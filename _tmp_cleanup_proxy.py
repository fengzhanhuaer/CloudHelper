from pathlib import Path


def find_block_end(text, start_idx):
    i = text.find('{', start_idx)
    if i == -1:
        raise ValueError('no opening brace found')
    depth = 0
    in_s = False
    in_d = False
    in_bt = False
    esc = False
    j = i
    n = len(text)
    while j < n:
        ch = text[j]
        if in_s:
            if esc:
                esc = False
            elif ch == '\\':
                esc = True
            elif ch == "'":
                in_s = False
        elif in_d:
            if esc:
                esc = False
            elif ch == '\\':
                esc = True
            elif ch == '"':
                in_d = False
        elif in_bt:
            if ch == '`':
                in_bt = False
        else:
            if ch == "'":
                in_s = True
            elif ch == '"':
                in_d = True
            elif ch == '`':
                in_bt = True
            elif ch == '{':
                depth += 1
            elif ch == '}':
                depth -= 1
                if depth == 0:
                    k = j + 1
                    while k < n and text[k] in '\r\n':
                        k += 1
                    return k
        j += 1
    raise ValueError('unbalanced braces')


def remove_block_by_signature(text, signature):
    idx = text.find(signature)
    if idx == -1:
        return text, False
    start = idx
    while start > 0 and text[start - 1] == '\n':
        start -= 1
    end = find_block_end(text, idx)
    return text[:start] + text[end:], True


def ensure_insert_after_function(text, func_sig, insert_text):
    if insert_text.strip() in text:
        return text
    idx = text.find(func_sig)
    if idx == -1:
        raise ValueError(f'cannot find function: {func_sig}')
    end = find_block_end(text, idx)
    return text[:end] + '\n' + insert_text.strip('\n') + '\n\n' + text[end:]


# --- network_assistant.go ---
na_path = Path('probe_manager/backend/network_assistant.go')
na = na_path.read_text(encoding='utf-8')

remove_sigs = [
    'func (s *networkAssistantService) ensureSocksServer() error {',
    'func (s *networkAssistantService) acceptLoop(ln net.Listener) {',
    'func (s *networkAssistantService) handleProxyConn(conn net.Conn) {',
    'func (s *networkAssistantService) handleSocksConn(conn net.Conn, br *bufio.Reader, remoteAddr string) {',
    'func (s *networkAssistantService) handleHTTPProxyConn(conn net.Conn, br *bufio.Reader, remoteAddr string) {',
    'type relayResult struct {',
    'func (s *networkAssistantService) logRelayClosed(relayType string, targetAddr string, result relayResult) {',
    'func relayErrorText(err error) string {',
    'func buildHTTPProxyForwardRequest(req *http.Request) (string, []byte, error) {',
    'func removeHopByHopHeaders(header http.Header) {',
    'func hostWithoutPort(targetAddr string) string {',
    'func normalizeProxyTargetAddress(rawHost string, defaultPort string) (string, error) {',
    'func writeHTTPProxyStatus(conn net.Conn, statusCode int, message string) error {',
    'func (s *networkAssistantService) handleSocksUDPAssociate(tcpConn net.Conn) {',
    'func dialUDPDirectPacket(targetAddr string, payload []byte) ([]byte, string, error) {',
    'func (s *networkAssistantService) applySystemProxy() error {',
    'func (s *networkAssistantService) stopProxyAndServer() error {',
    'func (s *networkAssistantService) restoreSystemProxyIfNeeded() error {',
    'func (s *networkAssistantService) stopSocksServerOnly() error {',
    'func readProxyRequest(br *bufio.Reader, conn net.Conn) (byte, socks5Request, error) {',
    'func replyProxySuccess(conn net.Conn, version byte) error {',
    'func replyProxyFailure(conn net.Conn, version byte) error {',
    'func socks5Handshake(br *bufio.Reader, conn net.Conn) error {',
    'func socks4ReadRequest(br *bufio.Reader, conn net.Conn) (socks5Request, error) {',
    'func readNullTerminated(br *bufio.Reader, maxLen int) (string, error) {',
    'func socks5ReadRequest(br *bufio.Reader, conn net.Conn) (socks5Request, error) {',
    'func socks5Reply(conn net.Conn, rep byte) error {',
    'func socks4Reply(conn net.Conn, rep byte) error {',
    'func socks5ReplyWithAddr(conn net.Conn, rep byte, bindAddr string) error {',
    'func parseSocks5UDPDatagram(packet []byte) (targetAddr string, payload []byte, err error) {',
    'func buildSocks5UDPDatagram(addr string, payload []byte) ([]byte, error) {',
]

for sig in remove_sigs:
    na, _ = remove_block_by_signature(na, sig)

insert_method = '''
func (s *networkAssistantService) stopTunnelMuxClients() error {
\ts.mu.Lock()
\tmuxClient := s.tunnelMuxClient
\textraMuxClients := make([]*tunnelMuxClient, 0, len(s.ruleMuxClients))
\tfor _, client := range s.ruleMuxClients {
\t\tif client != nil {
\t\t\textraMuxClients = append(extraMuxClients, client)
\t\t}
\t}
\ts.ruleMuxClients = make(map[string]*tunnelMuxClient)
\ts.tunnelMuxClient = nil
\ts.mu.Unlock()

\tif muxClient != nil {
\t\tmuxClient.close()
\t}
\tfor _, client := range extraMuxClients {
\t\tclient.close()
\t}
\treturn nil
}
'''
na = ensure_insert_after_function(na, 'func (s *networkAssistantService) Shutdown() error {', insert_method)
na_path.write_text(na, encoding='utf-8')

# --- system_proxy_windows.go ---
spw_path = Path('probe_manager/backend/system_proxy_windows.go')
spw = spw_path.read_text(encoding='utf-8')
spw, _ = remove_block_by_signature(spw, 'func applySocks5SystemProxy(socksAddr string) error {')
spw_path.write_text(spw, encoding='utf-8')

# --- system_proxy_other.go ---
spo_path = Path('probe_manager/backend/system_proxy_other.go')
spo = spo_path.read_text(encoding='utf-8')
spo, _ = remove_block_by_signature(spo, 'func applySocks5SystemProxy(_ string) error {')
spo_path.write_text(spo, encoding='utf-8')

# --- network_assistant_test.go ---
test_path = Path('probe_manager/backend/network_assistant_test.go')
txt = test_path.read_text(encoding='utf-8')
for sig in [
    'func TestSocks5HandshakeNoAuth(t *testing.T) {',
    'func TestSocks5ReadConnectRequestDomain(t *testing.T) {',
    'func TestSocks4ReadConnectRequestIPv4(t *testing.T) {',
    'func TestSocks4ReadConnectRequestDomain(t *testing.T) {',
]:
    txt, _ = remove_block_by_signature(txt, sig)
test_path.write_text(txt, encoding='utf-8')

print('ok')
