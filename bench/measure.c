/* Runs a program and writes its wall time, CPU time, peak RSS and exit status as one JSON line. */
#define _DEFAULT_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define MEASURE_FAILED 125

static double tv_seconds(struct timeval tv) {
	return (double)tv.tv_sec + (double)tv.tv_usec / 1e6;
}

int main(int argc, char **argv) {
	if (argc < 3) {
		fputs("usage: measure <stats-file> <program> [args...]\n", stderr);
		return 2;
	}
	/* The heap stays untouched: the child's ru_maxrss never reads below this process's RSS at fork. */
	int fd = open(argv[1], O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
	if (fd < 0) {
		fprintf(stderr, "measure: cannot open %s: %s\n", argv[1], strerror(errno));
		return MEASURE_FAILED;
	}

	struct timespec t0, t1;
	if (clock_gettime(CLOCK_MONOTONIC, &t0) != 0) {
		fprintf(stderr, "measure: clock_gettime: %s\n", strerror(errno));
		return MEASURE_FAILED;
	}
	pid_t pid = fork();
	if (pid < 0) {
		fprintf(stderr, "measure: fork: %s\n", strerror(errno));
		return MEASURE_FAILED;
	}
	if (pid == 0) {
		execvp(argv[2], argv + 2);
		fprintf(stderr, "measure: cannot exec %s: %s\n", argv[2], strerror(errno));
		_exit(127);
	}

	int status;
	struct rusage ru;
	pid_t got;
	do {
		got = wait4(pid, &status, 0, &ru);
	} while (got < 0 && errno == EINTR);
	if (got < 0) {
		fprintf(stderr, "measure: wait4: %s\n", strerror(errno));
		return MEASURE_FAILED;
	}
	if (clock_gettime(CLOCK_MONOTONIC, &t1) != 0) {
		fprintf(stderr, "measure: clock_gettime: %s\n", strerror(errno));
		return MEASURE_FAILED;
	}

	int code;
	if (WIFEXITED(status))
		code = WEXITSTATUS(status);
	else if (WIFSIGNALED(status))
		code = 128 + WTERMSIG(status);
	else
		code = MEASURE_FAILED;

	double wall = (double)(t1.tv_sec - t0.tv_sec) + (double)(t1.tv_nsec - t0.tv_nsec) / 1e9;
	char line[256];
	int n = snprintf(line, sizeof line, "{\"wall_s\":%.6f,\"user_s\":%.6f,\"sys_s\":%.6f,\"max_rss_kb\":%ld,\"exit\":%d}\n",
			 wall, tv_seconds(ru.ru_utime), tv_seconds(ru.ru_stime), ru.ru_maxrss, code);
	if (n < 0 || (size_t)n >= sizeof line || write(fd, line, (size_t)n) != (ssize_t)n || close(fd) != 0) {
		fprintf(stderr, "measure: writing %s failed\n", argv[1]);
		return MEASURE_FAILED;
	}
	return code;
}
