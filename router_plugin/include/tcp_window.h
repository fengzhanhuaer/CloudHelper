#ifndef VR_TCP_WINDOW_H
#define VR_TCP_WINDOW_H

#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <asm/socket.h>
#include <stddef.h>
#include <stdint.h>

/* 200 Mbps over 300 ms needs 7.5 MB in flight. Allow a third more for
   window and scheduling variation, with the same small initial budget. */
enum { VR_TCP_FLIGHT_BYTES = 200000 / 8 * 300,
       VR_TCP_TARGET_WINDOW = VR_TCP_FLIGHT_BYTES * 4 / 3,
       VR_TCP_START_WINDOW = 256 * 1024, VR_TCP_MIN_SHARE = 16 * 1024 };

typedef struct {
    uint64_t bytes, low_since, active_until;
    int limit, desired, target;
} vr_tcp_window_lane;

typedef struct { uint64_t sampled; vr_tcp_window_lane rx, tx; } vr_tcp_windows;
typedef struct { vr_tcp_windows *windows; int fd; } vr_tcp_window_socket;

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
        lane->limit = lane->desired = initial;
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
    int limit = lane->desired;
    lane->bytes = bytes;
    if (demand >= VR_TCP_MIN_SHARE / 4) lane->active_until = now + 1000;
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

static inline void vr_tcp_windows_sample(vr_tcp_windows *w, uint64_t now, uint64_t rx, uint64_t tx)
{
    if (now < w->sampled || now - w->sampled < 300) return;
    uint64_t elapsed = now - w->sampled;
    w->sampled = now;
    if (w->rx.limit) w->rx.desired = vr_tcp_window_next(&w->rx, rx, now, elapsed);
    if (w->tx.limit) w->tx.desired = vr_tcp_window_next(&w->tx, tx, now, elapsed);
}

static inline void vr_tcp_windows_plan(const vr_tcp_window_socket *sockets, size_t count, uint64_t now)
{
    size_t directions = 0;
    const uint64_t budget = VR_TCP_TARGET_WINDOW;
    for (size_t i = 0; i < count; i++)
        directions += !!sockets[i].windows->rx.limit + !!sockets[i].windows->tx.limit;
    if (!directions) return;
    uint64_t floor = budget / directions;
    if (floor > VR_TCP_MIN_SHARE) floor = VR_TCP_MIN_SHARE;
    uint64_t demand[2] = {0, 0};
    for (size_t i = 0; i < count; i++) {
        vr_tcp_window_lane *lanes[] = {&sockets[i].windows->rx, &sockets[i].windows->tx};
        for (size_t j = 0; j < 2; j++)
            if (lanes[j]->limit) demand[lanes[j]->active_until > now] += (unsigned)lanes[j]->desired - floor;
    }
    uint64_t remaining = budget - floor * directions;
    uint64_t share[2];
    share[1] = demand[1] < remaining ? demand[1] : remaining;
    remaining -= share[1];
    share[0] = demand[0] < remaining ? demand[0] : remaining;
    for (size_t i = 0; i < count; i++) {
        vr_tcp_window_lane *lanes[] = {&sockets[i].windows->rx, &sockets[i].windows->tx};
        for (size_t j = 0; j < 2; j++) {
            vr_tcp_window_lane *lane = lanes[j];
            if (!lane->limit) continue;
            unsigned active = lane->active_until > now;
            uint64_t extra = (unsigned)lane->desired - floor;
            lane->target = (int)(floor + (demand[active] ? extra * share[active] / demand[active] : 0));
        }
    }
}

static inline void vr_tcp_windows_share(const vr_tcp_window_socket *sockets, size_t count, uint64_t now)
{
    vr_tcp_windows_plan(sockets, count, now);
    const int options[] = {SO_RCVBUFFORCE, SO_SNDBUFFORCE};
    uint64_t committed = 0;
    for (size_t i = 0; i < count; i++)
        committed += (unsigned)sockets[i].windows->rx.limit + (unsigned)sockets[i].windows->tx.limit;
    for (int shrink = 1; shrink >= 0; shrink--) {
        for (size_t i = 0; i < count; i++) {
            vr_tcp_window_lane *lanes[] = {&sockets[i].windows->rx, &sockets[i].windows->tx};
            for (size_t j = 0; j < 2; j++) {
                vr_tcp_window_lane *lane = lanes[j];
                int change = lane->target - lane->limit;
                if (!lane->limit || !change || (change < 0) != shrink) continue;
                if (!shrink && committed + (unsigned)change > VR_TCP_TARGET_WINDOW) continue;
                if (setsockopt(sockets[i].fd, SOL_SOCKET, options[j], &lane->target, sizeof(lane->target)) == 0) {
                    committed = committed - (unsigned)lane->limit + (unsigned)lane->target;
                    lane->limit = lane->target;
                }
            }
        }
    }
}

#endif
