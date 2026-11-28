#ifndef VR_TCP_WINDOW_H
#define VR_TCP_WINDOW_H

#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <asm/socket.h>
#include <stdint.h>

/* 200 Mbps over 300 ms needs 7.5 MB in flight. Allow a third more for
   window and scheduling variation, with the same small initial budget. */
enum { VR_TCP_FLIGHT_BYTES = 200000 / 8 * 300,
       VR_TCP_TARGET_WINDOW = VR_TCP_FLIGHT_BYTES * 4 / 3,
       VR_TCP_START_WINDOW = 256 * 1024 };

typedef struct {
    uint64_t bytes, low_since;
    int limit;
} vr_tcp_window_lane;

typedef struct { uint64_t sampled; vr_tcp_window_lane rx, tx; } vr_tcp_windows;

static inline void vr_tcp_prepare_receive_window(int fd)
{
    int window = VR_TCP_TARGET_WINDOW;
    /* Before SYN: Linux doubles the socket budget for packet overhead. The
       advertised window starts small and grows as data is consumed. */
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVBUFFORCE, &window, sizeof(window));
    (void)setsockopt(fd, IPPROTO_TCP, TCP_WINDOW_CLAMP, &window, sizeof(window));
}

static inline void vr_tcp_window_start_lane(int fd, int option, vr_tcp_window_lane *lane, uint64_t bytes)
{
    int initial = VR_TCP_START_WINDOW;
    *lane = (vr_tcp_window_lane){.bytes = bytes};
    if (setsockopt(fd, SOL_SOCKET, option, &initial, sizeof(initial)) == 0)
        lane->limit = initial;
}

static inline void vr_tcp_windows_start(vr_tcp_windows *w, int fd, uint64_t now, uint64_t rx, uint64_t tx)
{
    w->sampled = now;
    vr_tcp_window_start_lane(fd, SO_RCVBUFFORCE, &w->rx, rx);
    vr_tcp_window_start_lane(fd, SO_SNDBUFFORCE, &w->tx, tx);
}

static inline int vr_tcp_window_next(vr_tcp_window_lane *lane, uint64_t bytes, uint64_t now, uint64_t elapsed)
{
    uint64_t demand = (bytes - lane->bytes) * 300 / elapsed;
    int limit = lane->limit;
    lane->bytes = bytes;
    if (demand >= (unsigned)limit / 4) {
        lane->low_since = 0;
        if (demand >= VR_TCP_TARGET_WINDOW / 4) return VR_TCP_TARGET_WINDOW;
        int desired = (int)demand * 4;
        if (desired < 2 * limit) desired = 2 * limit;
        return desired > VR_TCP_TARGET_WINDOW ? VR_TCP_TARGET_WINDOW : desired;
    }
    if (demand <= (unsigned)limit / 8 && limit > VR_TCP_START_WINDOW) {
        if (!lane->low_since) lane->low_since = now;
        if (now - lane->low_since >= 30000) {
            lane->low_since = now;
            return limit / 2 < VR_TCP_START_WINDOW ? VR_TCP_START_WINDOW : limit / 2;
        }
    } else {
        lane->low_since = 0;
    }
    return limit;
}

static inline void vr_tcp_window_tick_lane(int fd, int option, vr_tcp_window_lane *lane,
                                           uint64_t bytes, uint64_t now, uint64_t elapsed)
{
    if (!lane->limit) return;
    int next = vr_tcp_window_next(lane, bytes, now, elapsed);
    if (next != lane->limit && setsockopt(fd, SOL_SOCKET, option, &next, sizeof(next)) == 0)
        lane->limit = next;
}

static inline void vr_tcp_windows_tick(vr_tcp_windows *w, int fd, uint64_t now, uint64_t rx, uint64_t tx)
{
    if (now < w->sampled || now - w->sampled < 300) return;
    uint64_t elapsed = now - w->sampled;
    w->sampled = now;
    vr_tcp_window_tick_lane(fd, SO_RCVBUFFORCE, &w->rx, rx, now, elapsed);
    vr_tcp_window_tick_lane(fd, SO_SNDBUFFORCE, &w->tx, tx, now, elapsed);
}

#endif
