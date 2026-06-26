#define _GNU_SOURCE
#include <time.h>
#include <pthread.h>
#include <stdint.h>

int pthread_cond_clockwait(pthread_cond_t *cond, pthread_mutex_t *mutex,
                            clockid_t clock_id,
                            const struct timespec *abstime) {
    (void)clock_id;
    struct timespec mono_now, real_now, rel, real_abs;
    clock_gettime(CLOCK_MONOTONIC, &mono_now);
    clock_gettime(CLOCK_REALTIME, &real_now);
    rel.tv_sec = abstime->tv_sec - mono_now.tv_sec;
    rel.tv_nsec = abstime->tv_nsec - mono_now.tv_nsec;
    if (rel.tv_nsec < 0) {
        rel.tv_sec--;
        rel.tv_nsec += 1000000000L;
    }
    real_abs.tv_sec = real_now.tv_sec + rel.tv_sec;
    real_abs.tv_nsec = real_now.tv_nsec + rel.tv_nsec;
    if (real_abs.tv_nsec >= 1000000000L) {
        real_abs.tv_sec++;
        real_abs.tv_nsec -= 1000000000L;
    }
    return pthread_cond_timedwait(cond, mutex, &real_abs);
}
