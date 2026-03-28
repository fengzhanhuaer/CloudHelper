package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const deletedProbeNodeNosFile = "deleted_probe_node_nos.json"

type deletedProbeNodeNosPayload struct {
	NodeNos []int `json:"node_nos"`
}

func deletedProbeNodeNosFilePath() (string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, deletedProbeNodeNosFile), nil
}

func loadDeletedProbeNodeNos() (map[int]struct{}, error) {
	path, err := deletedProbeNodeNosFilePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[int]struct{}), nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return make(map[int]struct{}), nil
	}
	var payload deletedProbeNodeNosPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	set := make(map[int]struct{}, len(payload.NodeNos))
	for _, no := range payload.NodeNos {
		if no > 0 {
			set[no] = struct{}{}
		}
	}
	return set, nil
}

func saveDeletedProbeNodeNos(set map[int]struct{}) error {
	path, err := deletedProbeNodeNosFilePath()
	if err != nil {
		return err
	}
	nos := make([]int, 0, len(set))
	for no := range set {
		nos = append(nos, no)
	}
	sort.Ints(nos)
	payload := deletedProbeNodeNosPayload{NodeNos: nos}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// GetDeletedProbeNodeNos 返回本地标记为"已删除"的探针节点号列表。
func (a *App) GetDeletedProbeNodeNos() ([]int, error) {
	set, err := loadDeletedProbeNodeNos()
	if err != nil {
		return nil, err
	}
	nos := make([]int, 0, len(set))
	for no := range set {
		nos = append(nos, no)
	}
	sort.Ints(nos)
	return nos, nil
}

// MarkProbeNodeDeleted 将探针节点号加入本地删除列表。节点仍存在于主控，仅本地标记。
func (a *App) MarkProbeNodeDeleted(nodeNo int) error {
	if nodeNo <= 0 {
		return errors.New("invalid node number")
	}
	set, err := loadDeletedProbeNodeNos()
	if err != nil {
		return err
	}
	set[nodeNo] = struct{}{}
	return saveDeletedProbeNodeNos(set)
}

// RestoreDeletedProbeNode 将探针节点号从本地删除列表中移除，恢复显示。
func (a *App) RestoreDeletedProbeNode(nodeNo int) error {
	if nodeNo <= 0 {
		return errors.New("invalid node number")
	}
	set, err := loadDeletedProbeNodeNos()
	if err != nil {
		return err
	}
	delete(set, nodeNo)
	return saveDeletedProbeNodeNos(set)
}
