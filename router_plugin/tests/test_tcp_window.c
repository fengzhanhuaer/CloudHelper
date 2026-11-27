#define _GNU_SOURCE
#include "tcp_window.h"
#include <assert.h>
#include <stdio.h>
#include <unistd.h>

static void check_decay(void)
{
    vr_tcp_window_lane lane = {.limit = VR_TCP_START_WINDOW};
    uint64_t bytes = VR_TCP_START_WINDOW / 4;
    lane.limit = vr_tcp_window_next(&lane, bytes, 300, 300);
    assert(lane.limit == 2 * VR_TCP_START_WINDOW);
    bytes += VR_TCP_TARGET_WINDOW;
    lane.limit = vr_tcp_window_next(&lane, bytes, 600, 300);
    assert(lane.limit == VR_TCP_TARGET_WINDOW);
    assert(vr_tcp_window_next(&lane, bytes, 900, 300) == lane.limit);
    assert(vr_tcp_window_next(&lane, bytes, 30600, 29700) == lane.limit);
    bytes += VR_TCP_TARGET_WINDOW;
    assert(vr_tcp_window_next(&lane, bytes, 30900, 300) == lane.limit);
    assert(vr_tcp_window_next(&lane, bytes, 31200, 300) == lane.limit);
    for (uint64_t now = 61200; lane.limit > VR_TCP_START_WINDOW; now += 30000) {
        int next = vr_tcp_window_next(&lane, bytes, now, 30000);
        assert(next == (lane.limit / 2 > VR_TCP_START_WINDOW ? lane.limit / 2 : VR_TCP_START_WINDOW));
        lane.limit = next;
    }
}

int main(void)
{
    check_decay();
    int listener = socket(AF_INET, SOCK_STREAM, 0);
    int client = socket(AF_INET, SOCK_STREAM, 0);
    assert(listener >= 0 && client >= 0);
    vr_tcp_prepare_receive_window(listener);
    vr_tcp_prepare_receive_window(client);
    struct sockaddr_in local = {.sin_family = AF_INET, .sin_addr.s_addr = htonl(INADDR_LOOPBACK)};
    socklen_t length = sizeof(local);
    assert(bind(listener, (struct sockaddr *)&local, length) == 0 && listen(listener, 1) == 0);
    assert(getsockname(listener, (struct sockaddr *)&local, &length) == 0);
    assert(connect(client, (struct sockaddr *)&local, length) == 0);
    int server = accept(listener, NULL, NULL);
    assert(server >= 0);
    int core_max = 0, tcp_max = 0, scaling = 0;
    FILE *file = fopen("/proc/sys/net/core/rmem_max", "r");
    assert(file && fscanf(file, "%d", &core_max) == 1);
    fclose(file);
    file = fopen("/proc/sys/net/ipv4/tcp_rmem", "r");
    assert(file && fscanf(file, "%*d %*d %d", &tcp_max) == 1);
    fclose(file);
    file = fopen("/proc/sys/net/ipv4/tcp_window_scaling", "r");
    assert(file && fscanf(file, "%d", &scaling) == 1);
    fclose(file);
    int expected = core_max > tcp_max ? core_max : tcp_max;
    if (expected > VR_TCP_TARGET_WINDOW) expected = VR_TCP_TARGET_WINDOW;
    for (int i = 0; i < 2; i++) {
        int fd = i ? client : server, budget;
        struct tcp_info info = {0};
        length = sizeof(info);
        assert(getsockopt(fd, IPPROTO_TCP, TCP_INFO, &info, &length) == 0);
        length = sizeof(budget);
        assert(getsockopt(fd, SOL_SOCKET, SO_RCVBUF, &budget, &length) == 0);
        printf("%s receive_scale=%u handshake_buffer=%d", i ? "client" : "server", info.tcpi_rcv_wscale, budget);
        if (scaling && budget >= 2 * VR_TCP_TARGET_WINDOW)
            assert((65535u << info.tcpi_rcv_wscale) >= (unsigned)expected);
        vr_tcp_windows w = {0};
        vr_tcp_windows_start(&w, fd, 0, 0, 0);
        if (w.rx.limit) {
            assert(getsockopt(fd, SOL_SOCKET, SO_RCVBUF, &budget, &length) == 0);
            assert(budget == 2 * VR_TCP_START_WINDOW);
            vr_tcp_windows_tick(&w, fd, 300, VR_TCP_TARGET_WINDOW, 0);
            assert(w.rx.limit == VR_TCP_TARGET_WINDOW && w.tx.limit == VR_TCP_START_WINDOW);
        }
        printf(" established_buffer=%d\n", budget);
    }
    close(server);
    close(client);
    close(listener);
    return 0;
}
