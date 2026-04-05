package backend

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	rulePolicyFile      = "rule_policies.txt"
	legacyRuleGroupFile = "rule_groups.txt"

	ruleFallbackGroupKey = "@fallback"

	rulePolicyActionDirect = "direct"
	rulePolicyActionReject = "reject"
	rulePolicyActionTunnel = "tunnel"
)

var defaultRulePolicies = []string{
	"@fallback,direct",
}

func isDirectRuleGroupKey(group string) bool {
	return strings.EqualFold(strings.TrimSpace(group), "direct")
}

type ruleGroupPolicy struct {
	Action       string
	TunnelNodeID string
}

type ruleRouteRejectError struct {
	Group string
}

func (e *ruleRouteRejectError) Error() string {
	group := strings.TrimSpace(e.Group)
	if group == "" || strings.EqualFold(group, ruleFallbackGroupKey) {
		return "rule fallback policy rejected target"
	}
	return "rule group rejected target: " + group
}

func isRuleRouteRejectErr(err error) bool {
	var target *ruleRouteRejectError
	return errors.As(err, &target)
}

type NetworkAssistantRuleGroupConfig struct {
	Group              string            `json:"group"`
	Action             string            `json:"action"`
	TunnelNodeID       string            `json:"tunnel_node_id,omitempty"`
	TunnelOptions      []string          `json:"tunnel_options"`
	TunnelOptionLabels map[string]string `json:"tunnel_option_labels,omitempty"`
}

type NetworkAssistantRuleConfig struct {
	RuleFilePath string                            `json:"rule_file_path"`
	Groups       []NetworkAssistantRuleGroupConfig `json:"groups"`
	Fallback     NetworkAssistantRuleGroupConfig   `json:"fallback"`
}

func (a *App) GetNetworkAssistantRuleConfig() (NetworkAssistantRuleConfig, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantRuleConfig{}, errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.GetRuleConfig()
}

func (a *App) SetNetworkAssistantRulePolicy(group, action, tunnelNodeID string) (NetworkAssistantRuleConfig, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantRuleConfig{}, errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.SetRulePolicy(group, action, tunnelNodeID)
}

func (s *networkAssistantService) GetRuleConfig() (NetworkAssistantRuleConfig, error) {
	s.refreshAvailableNodesForRuleConfig()

	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		return NetworkAssistantRuleConfig{}, err
	}

	s.mu.Lock()
	s.ruleRouting = routing
	availableNodes := append([]string(nil), s.availableNodes...)
	chainTargets := copyProbeChainTargets(s.chainTargets)
	currentNode := strings.TrimSpace(s.nodeID)
	s.mu.Unlock()

	tunnelOptions := buildRuleTunnelOptions(availableNodes, currentNode)
	if currentNode == "" {
		currentNode = defaultNodeID
	}
	return buildRuleConfigFromRouting(routing, tunnelOptions, currentNode, chainTargets), nil
}

func (s *networkAssistantService) SetRulePolicy(group, action, tunnelNodeID string) (NetworkAssistantRuleConfig, error) {
	s.refreshAvailableNodesForRuleConfig()

	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		return NetworkAssistantRuleConfig{}, err
	}

	s.mu.RLock()
	availableNodes := append([]string(nil), s.availableNodes...)
	chainTargets := copyProbeChainTargets(s.chainTargets)
	currentNode := strings.TrimSpace(s.nodeID)
	s.mu.RUnlock()
	if currentNode == "" {
		currentNode = defaultNodeID
	}
	tunnelOptions := buildRuleTunnelOptions(availableNodes, currentNode)

	targetKey := normalizeRulePolicyGroupKey(group)
	if targetKey == "" {
		return NetworkAssistantRuleConfig{}, errors.New("rule group is required")
	}
	groups := extractRuleGroupsFromRuleSet(routing.RuleSet)
	if targetKey != ruleFallbackGroupKey && !containsNodeID(groups, targetKey) {
		return NetworkAssistantRuleConfig{}, fmt.Errorf("rule group not found in rule_routes: %s", targetKey)
	}

	previousPolicy, err := readRulePolicyForGroup(routing, targetKey, currentNode, tunnelOptions)
	if err != nil {
		return NetworkAssistantRuleConfig{}, err
	}

	normalizedPolicy, err := normalizeRuleGroupPolicy(
		ruleGroupPolicy{
			Action:       action,
			TunnelNodeID: tunnelNodeID,
		},
		currentNode,
		tunnelOptions,
		defaultPolicyActionForGroupKey(targetKey),
	)
	if err != nil {
		return NetworkAssistantRuleConfig{}, err
	}
	needClearDynamicBypass := shouldClearDynamicBypassForPolicyTransition(previousPolicy, normalizedPolicy)

	if routing.GroupNodeMap == nil {
		routing.GroupNodeMap = make(map[string]string)
	}
	routing.GroupNodeMap[targetKey] = encodeRuleGroupPolicy(normalizedPolicy)
	routing.GroupNodeMap = buildCanonicalRulePolicyMap(routing.RuleSet, routing.GroupNodeMap, currentNode)

	if err := saveTunnelRulePolicyFile(routing.GroupFilePath, routing.RuleSet, routing.GroupNodeMap); err != nil {
		return NetworkAssistantRuleConfig{}, err
	}

	s.mu.Lock()
	s.ruleRouting = routing
	s.ruleDNSCache = make(map[string]dnsCacheEntry)
	tunModeEnabled := s.mode == networkModeTUN && s.tunEnabled
	s.mu.Unlock()

	cfg := buildRuleConfigFromRouting(routing, tunnelOptions, currentNode, chainTargets)
	if needClearDynamicBypass && tunModeEnabled {
		if err := s.clearTUNDynamicBypassRoutes(); err != nil {
			return cfg, err
		}
		s.logf("rule policy switched from direct, dynamic tun bypass routes cleared: group=%s", targetKey)
	}

	return cfg, nil
}

func buildRuleConfigFromRouting(routing tunnelRuleRouting, tunnelOptions []string, defaultNode string, chainTargets map[string]probeChainEndpoint) NetworkAssistantRuleConfig {
	filteredTunnelOptions := filterRuleTunnelOptions(tunnelOptions)
	groups := extractRuleGroupsFromRuleSet(routing.RuleSet)
	items := make([]NetworkAssistantRuleGroupConfig, 0, len(groups))
	for _, group := range groups {
		if isDirectRuleGroupKey(group) {
			continue
		}
		policy, _ := readRulePolicyForGroup(routing, group, defaultNode, filteredTunnelOptions)
		groupOptions := mergeRuleTunnelOptions(filteredTunnelOptions, policy.TunnelNodeID)
		groupOptionLabels := buildRuleTunnelOptionLabels(groupOptions, chainTargets)
		items = append(items, NetworkAssistantRuleGroupConfig{
			Group:              group,
			Action:             policy.Action,
			TunnelNodeID:       policy.TunnelNodeID,
			TunnelOptions:      groupOptions,
			TunnelOptionLabels: groupOptionLabels,
		})
	}

	fallbackPolicy, _ := readRulePolicyForGroup(routing, ruleFallbackGroupKey, defaultNode, filteredTunnelOptions)
	fallbackOptions := mergeRuleTunnelOptions(filteredTunnelOptions, fallbackPolicy.TunnelNodeID)
	fallbackOptionLabels := buildRuleTunnelOptionLabels(fallbackOptions, chainTargets)
	return NetworkAssistantRuleConfig{
		RuleFilePath: strings.TrimSpace(routing.RuleFilePath),
		Groups:       items,
		Fallback: NetworkAssistantRuleGroupConfig{
			Group:              ruleFallbackGroupKey,
			Action:             fallbackPolicy.Action,
			TunnelNodeID:       fallbackPolicy.TunnelNodeID,
			TunnelOptions:      fallbackOptions,
			TunnelOptionLabels: fallbackOptionLabels,
		},
	}
}

func copyProbeChainTargets(source map[string]probeChainEndpoint) map[string]probeChainEndpoint {
	if len(source) == 0 {
		return map[string]probeChainEndpoint{}
	}
	targets := make(map[string]probeChainEndpoint, len(source))
	for key, endpoint := range source {
		targets[key] = endpoint
	}
	return targets
}

func buildRuleTunnelOptionLabels(tunnelOptions []string, chainTargets map[string]probeChainEndpoint) map[string]string {
	labels := make(map[string]string, len(tunnelOptions))
	for _, rawNodeID := range tunnelOptions {
		nodeID := strings.TrimSpace(rawNodeID)
		if nodeID == "" {
			continue
		}
		labels[nodeID] = resolveRuleTunnelOptionLabel(nodeID, chainTargets)
	}
	return labels
}

func resolveRuleTunnelOptionLabel(nodeID string, chainTargets map[string]probeChainEndpoint) string {
	cleanNodeID := strings.TrimSpace(nodeID)
	if cleanNodeID == "" {
		return ""
	}
	if strings.EqualFold(cleanNodeID, defaultNodeID) {
		return "直连"
	}
	if chainID, ok := parseChainTargetNodeID(cleanNodeID); ok {
		target, found := chainTargets[cleanNodeID]
		if !found {
			target, found = chainTargets[buildChainTargetNodeID(chainID)]
		}
		if found {
			chainName := strings.TrimSpace(target.ChainName)
			if chainName != "" {
				return chainName
			}
		}
		if chainID != "" {
			return "链路 " + chainID
		}
	}
	return cleanNodeID
}

func buildRuleTunnelOptions(availableNodes []string, currentNode string) []string {
	options := make([]string, 0, len(availableNodes)+2)
	add := func(raw string) {
		node := strings.TrimSpace(raw)
		if node == "" || containsNodeID(options, node) {
			return
		}
		options = append(options, node)
	}

	for _, node := range availableNodes {
		add(node)
	}
	add(currentNode)
	add(defaultNodeID)
	return filterRuleTunnelOptions(options)
}

func filterRuleTunnelOptions(nodes []string) []string {
	options := make([]string, 0, len(nodes))
	for _, raw := range nodes {
		node := strings.TrimSpace(raw)
		if node == "" || containsNodeID(options, node) {
			continue
		}
		if strings.EqualFold(node, defaultNodeID) {
			options = append(options, node)
			continue
		}
		if _, ok := parseChainTargetNodeID(node); ok {
			options = append(options, node)
		}
	}
	if len(options) == 0 {
		options = append(options, defaultNodeID)
	}
	return options
}

func mergeRuleTunnelOptions(base []string, selected string) []string {
	options := append([]string(nil), base...)
	selectedNode := strings.TrimSpace(selected)
	if selectedNode == "" || containsNodeID(options, selectedNode) {
		return options
	}
	if !strings.EqualFold(selectedNode, defaultNodeID) {
		if _, ok := parseChainTargetNodeID(selectedNode); !ok {
			return options
		}
	}
	return append(options, selectedNode)
}

func defaultPolicyActionForGroupKey(group string) string {
	cleanGroup := strings.TrimSpace(group)
	if strings.EqualFold(cleanGroup, ruleFallbackGroupKey) || isDirectRuleGroupKey(cleanGroup) {
		return rulePolicyActionDirect
	}
	return rulePolicyActionTunnel
}

func normalizeRulePolicyGroupKey(raw string) string {
	group := normalizeRuleGroupName(raw)
	if group == "" {
		return ""
	}
	if strings.EqualFold(group, ruleFallbackGroupKey) || strings.EqualFold(group, "fallback") || group == "兜底" || group == "兜底组" {
		return ruleFallbackGroupKey
	}
	return group
}

func normalizeRulePolicyAction(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case rulePolicyActionDirect:
		return rulePolicyActionDirect
	case rulePolicyActionReject:
		return rulePolicyActionReject
	case rulePolicyActionTunnel:
		return rulePolicyActionTunnel
	default:
		return ""
	}
}

func normalizeRuleGroupPolicy(policy ruleGroupPolicy, defaultNode string, tunnelOptions []string, defaultAction string) (ruleGroupPolicy, error) {
	action := normalizeRulePolicyAction(policy.Action)
	if action == "" {
		action = normalizeRulePolicyAction(defaultAction)
	}
	if action == "" {
		action = rulePolicyActionTunnel
	}

	normalized := ruleGroupPolicy{Action: action}
	switch action {
	case rulePolicyActionDirect, rulePolicyActionReject:
		return normalized, nil
	case rulePolicyActionTunnel:
		node := strings.TrimSpace(policy.TunnelNodeID)
		if node == "" {
			node = strings.TrimSpace(defaultNode)
		}
		if node == "" {
			node = defaultNodeID
		}
		normalized.TunnelNodeID = strings.TrimSpace(node)
		if normalized.TunnelNodeID == "" {
			normalized.TunnelNodeID = defaultNodeID
		}
		if !strings.EqualFold(normalized.TunnelNodeID, defaultNodeID) {
			if _, ok := parseChainTargetNodeID(normalized.TunnelNodeID); !ok {
				normalized.TunnelNodeID = defaultNodeID
			}
		}
		return normalized, nil
	default:
		return ruleGroupPolicy{}, fmt.Errorf("unsupported rule action: %s", action)
	}
}

func encodeRuleGroupPolicy(policy ruleGroupPolicy) string {
	action := normalizeRulePolicyAction(policy.Action)
	switch action {
	case rulePolicyActionDirect:
		return rulePolicyActionDirect
	case rulePolicyActionReject:
		return rulePolicyActionReject
	case rulePolicyActionTunnel:
		node := strings.TrimSpace(policy.TunnelNodeID)
		if node == "" {
			node = defaultNodeID
		}
		return rulePolicyActionTunnel + ":" + node
	default:
		return rulePolicyActionTunnel + ":" + defaultNodeID
	}
}

func decodeRuleGroupPolicy(rawValue string) ruleGroupPolicy {
	value := strings.TrimSpace(rawValue)
	if value == "" {
		return ruleGroupPolicy{}
	}
	if strings.HasPrefix(strings.ToLower(value), rulePolicyActionTunnel+":") {
		return ruleGroupPolicy{
			Action:       rulePolicyActionTunnel,
			TunnelNodeID: strings.TrimSpace(value[len(rulePolicyActionTunnel)+1:]),
		}
	}
	action := normalizeRulePolicyAction(value)
	if action != "" {
		return ruleGroupPolicy{Action: action}
	}
	// legacy rule_groups value: plain node id / chain id
	return ruleGroupPolicy{
		Action:       rulePolicyActionTunnel,
		TunnelNodeID: value,
	}
}

func readRulePolicyForGroup(routing tunnelRuleRouting, group string, defaultNode string, tunnelOptions []string) (ruleGroupPolicy, error) {
	key := normalizeRulePolicyGroupKey(group)
	if key == "" {
		return ruleGroupPolicy{}, errors.New("empty rule group")
	}

	if isDirectRuleGroupKey(key) {
		return ruleGroupPolicy{Action: rulePolicyActionDirect}, nil
	}

	defaultAction := defaultPolicyActionForGroupKey(key)
	rawValue := ""
	if routing.GroupNodeMap != nil {
		rawValue = strings.TrimSpace(routing.GroupNodeMap[key])
	}
	return normalizeRuleGroupPolicy(decodeRuleGroupPolicy(rawValue), defaultNode, tunnelOptions, defaultAction)
}

func shouldClearDynamicBypassForPolicyTransition(previous, next ruleGroupPolicy) bool {
	return normalizeRulePolicyAction(previous.Action) == rulePolicyActionDirect && normalizeRulePolicyAction(next.Action) != rulePolicyActionDirect
}

func extractRuleGroupsFromRuleSet(ruleSet tunnelRuleSet) []string {
	seen := make(map[string]struct{})
	groups := make([]string, 0)
	for _, rule := range ruleSet.Rules {
		group := normalizeRuleGroupName(rule.Group)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	return groups
}

func buildCanonicalRulePolicyMap(ruleSet tunnelRuleSet, input map[string]string, defaultNode string) map[string]string {
	tunnelOptions := buildRuleTunnelOptions(nil, defaultNode)
	groups := extractRuleGroupsFromRuleSet(ruleSet)
	result := make(map[string]string, len(groups)+1)
	for _, group := range groups {
		if isDirectRuleGroupKey(group) {
			result[group] = encodeRuleGroupPolicy(ruleGroupPolicy{Action: rulePolicyActionDirect})
			continue
		}
		raw := ""
		if input != nil {
			raw = strings.TrimSpace(input[group])
		}
		policy, err := normalizeRuleGroupPolicy(decodeRuleGroupPolicy(raw), defaultNode, tunnelOptions, rulePolicyActionTunnel)
		if err != nil {
			policy = ruleGroupPolicy{Action: rulePolicyActionTunnel, TunnelNodeID: defaultNode}
		}
		result[group] = encodeRuleGroupPolicy(policy)
	}

	fallbackRaw := ""
	if input != nil {
		fallbackRaw = strings.TrimSpace(input[ruleFallbackGroupKey])
	}
	fallbackPolicy, err := normalizeRuleGroupPolicy(decodeRuleGroupPolicy(fallbackRaw), defaultNode, tunnelOptions, rulePolicyActionDirect)
	if err != nil {
		fallbackPolicy = ruleGroupPolicy{Action: rulePolicyActionDirect}
	}
	result[ruleFallbackGroupKey] = encodeRuleGroupPolicy(fallbackPolicy)
	return result
}

func ensureTunnelRulePolicyFile(policyPath string) error {
	_, err := os.Stat(policyPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	content := "# CloudHelper rule group policies\n" +
		"# each line: <proxy_group|@fallback>,<direct|reject|tunnel>[,<node_id_or_chain_id>]\n" +
		"# examples:\n" +
		strings.Join(defaultRulePolicies, "\n") + "\n"
	if err := os.WriteFile(policyPath, []byte(content), 0o644); err != nil {
		return err
	}
	return autoBackupManagerData()
}

func parseTunnelRulePolicyFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	policyMap := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid rule_policies line %d: expected <group>,<action>[,<node_id_or_chain_id>]", lineNo)
		}
		groupKey := normalizeRulePolicyGroupKey(parts[0])
		if groupKey == "" {
			return nil, fmt.Errorf("invalid rule_policies line %d: group is required", lineNo)
		}
		action := normalizeRulePolicyAction(parts[1])
		if action == "" {
			return nil, fmt.Errorf("invalid rule_policies line %d: unsupported action", lineNo)
		}
		nodeID := ""
		if len(parts) >= 3 {
			nodeID = strings.TrimSpace(parts[2])
		}
		policyMap[groupKey] = encodeRuleGroupPolicy(ruleGroupPolicy{Action: action, TunnelNodeID: nodeID})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return policyMap, nil
}

func saveTunnelRulePolicyFile(path string, ruleSet tunnelRuleSet, policyMap map[string]string) error {
	groups := extractRuleGroupsFromRuleSet(ruleSet)
	lines := make([]string, 0, len(groups)+4)
	lines = append(lines,
		"# CloudHelper rule group policies",
		"# each line: <proxy_group|@fallback>,<direct|reject|tunnel>[,<node_id_or_chain_id>]",
	)
	for _, group := range groups {
		policy := decodeRuleGroupPolicy(policyMap[group])
		switch normalizeRulePolicyAction(policy.Action) {
		case rulePolicyActionDirect:
			lines = append(lines, group+","+rulePolicyActionDirect)
		case rulePolicyActionReject:
			lines = append(lines, group+","+rulePolicyActionReject)
		default:
			node := strings.TrimSpace(policy.TunnelNodeID)
			if node == "" {
				node = defaultNodeID
			}
			lines = append(lines, group+","+rulePolicyActionTunnel+","+node)
		}
	}

	fallback := decodeRuleGroupPolicy(policyMap[ruleFallbackGroupKey])
	switch normalizeRulePolicyAction(fallback.Action) {
	case rulePolicyActionDirect:
		lines = append(lines, ruleFallbackGroupKey+","+rulePolicyActionDirect)
	case rulePolicyActionReject:
		lines = append(lines, ruleFallbackGroupKey+","+rulePolicyActionReject)
	default:
		node := strings.TrimSpace(fallback.TunnelNodeID)
		if node == "" {
			node = defaultNodeID
		}
		lines = append(lines, ruleFallbackGroupKey+","+rulePolicyActionTunnel+","+node)
	}

	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

func loadLegacyRuleGroupPolicyMap(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	legacyMap, err := parseTunnelRuleGroupFile(path)
	if err != nil {
		return nil, err
	}
	converted := make(map[string]string, len(legacyMap))
	for group, nodeID := range legacyMap {
		normalizedGroup := normalizeRuleGroupName(group)
		if normalizedGroup == "" {
			continue
		}
		converted[normalizedGroup] = encodeRuleGroupPolicy(ruleGroupPolicy{
			Action:       rulePolicyActionTunnel,
			TunnelNodeID: strings.TrimSpace(nodeID),
		})
	}
	return converted, nil
}

func resolveRulePolicyPaths(dataDir string) (string, string) {
	policyPath := filepath.Join(dataDir, rulePolicyFile)
	legacyPath := filepath.Join(dataDir, legacyRuleGroupFile)
	return policyPath, legacyPath
}

func (s *networkAssistantService) refreshAvailableNodesForRuleConfig() {
	if s == nil {
		return
	}
	if err := s.refreshAvailableNodes(); err != nil {
		s.logf("refresh available nodes for rule config failed: %v", err)
	}
}
