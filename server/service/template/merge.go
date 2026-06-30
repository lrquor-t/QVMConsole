package template

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normalizeMergeMode 归一化合并模式，非法值返回错误。
func normalizeMergeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case TemplateMergeModeFlatten:
		return TemplateMergeModeFlatten, nil
	case TemplateMergeModeCommitToParent:
		return TemplateMergeModeCommitToParent, nil
	default:
		return "", fmt.Errorf("不支持的合并模式: %s", mode)
	}
}

// ParseMergeTemplateParams 从 JSON 解析合并参数并归一化模式。
func ParseMergeTemplateParams(jsonStr string) (*MergeTemplateParams, error) {
	var params MergeTemplateParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	params.TemplateName = strings.TrimSpace(params.TemplateName)
	if params.TemplateName == "" {
		return nil, fmt.Errorf("参数解析失败: template_name 为空")
	}
	mode, err := normalizeMergeMode(params.Mode)
	if err != nil {
		return nil, err
	}
	params.Mode = mode
	return &params, nil
}

// vmStateOrUnknown 返回状态，空则 unknown（不依赖 HookFirstNonEmpty，便于单测）。
func vmStateOrUnknown(status string) string {
	if s := strings.TrimSpace(status); s != "" {
		return s
	}
	return "unknown"
}

// buildFlattenBlockers 计算模式一（平铺）阻塞项。
func buildFlattenBlockers(hasBacking bool, subtreeVMs []TemplateRelatedVM) []string {
	if !hasBacking {
		return []string{"模板已是独立镜像，无需合并"}
	}
	var blockers []string
	for _, vm := range subtreeVMs {
		if !isVMStateShutoff(vm.Status) {
			blockers = append(blockers, fmt.Sprintf("关联虚拟机 %s 当前状态为 %s，请先关机", vm.Name, vmStateOrUnknown(vm.Status)))
		}
	}
	return blockers
}

// buildCommitBlockers 计算模式二（回写父模板）阻塞项。
func buildCommitBlockers(hasParent bool, parentDirectVMs []TemplateRelatedVM, parentOtherChildren []TemplateInfo, subtreeVMs []TemplateRelatedVM) []string {
	var blockers []string
	if !hasParent {
		blockers = append(blockers, "根模板没有父节点，无法回写")
	}
	if len(parentDirectVMs) > 0 {
		names := make([]string, 0, len(parentDirectVMs))
		for _, vm := range parentDirectVMs {
			names = append(names, vm.Name)
		}
		blockers = append(blockers, fmt.Sprintf("父模板存在直接依赖虚拟机（%s），不允许回写", strings.Join(names, "、")))
	}
	if len(parentOtherChildren) > 0 {
		names := make([]string, 0, len(parentOtherChildren))
		for _, c := range parentOtherChildren {
			names = append(names, c.Name)
		}
		blockers = append(blockers, fmt.Sprintf("父模板存在其它子模板（%s），不允许回写", strings.Join(names, "、")))
	}
	for _, vm := range subtreeVMs {
		if !isVMStateShutoff(vm.Status) {
			blockers = append(blockers, fmt.Sprintf("关联虚拟机 %s 当前状态为 %s，请先关机", vm.Name, vmStateOrUnknown(vm.Status)))
		}
	}
	return blockers
}

// parentOtherChildrenList 返回父模板下除指定节点外的其它子模板。
func parentOtherChildrenList(tree *templateTreeData, parent *TemplateInfo, excludeNodeID string) []TemplateInfo {
	if parent == nil {
		return nil
	}
	children := directChildTemplates(tree, parent.NodeID)
	out := make([]TemplateInfo, 0, len(children))
	for _, c := range children {
		if c.NodeID != excludeNodeID {
			out = append(out, c)
		}
	}
	return out
}

// GetMergePreview 返回模板合并预览（供前端渲染确认弹窗）。
func GetMergePreview(templateName string) (*MergePreview, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	tpl, ok := tree.byName[templateName]
	if !ok {
		return nil, fmt.Errorf("模板不存在: %s", templateName)
	}

	var parent *TemplateInfo
	if pid := strings.TrimSpace(tpl.ParentNodeID); pid != "" {
		if p, ok := tree.byNodeID[pid]; ok {
			pp := p
			parent = &pp
		}
	}

	// 物理上是否有 backing（元数据 ParentNodeID 与物理 backing 任一判定）。
	hasBacking := strings.TrimSpace(tpl.ParentNodeID) != ""
	if chain, qErr := HookQemuInfoChain(tpl.Path); qErr == nil && len(chain) >= 2 {
		hasBacking = true
	} else if qErr != nil {
		// qemu-img 读取失败时不阻断预览，仅以元数据为准。
	}

	subtreeVMs := hydrateTemplateRelatedVMs(collectTemplateSubtreeVMs(tree, tpl.NodeID))
	flattenBlockers := buildFlattenBlockers(hasBacking, subtreeVMs)

	var parentDirectVMs []TemplateRelatedVM
	if parent != nil {
		parentDirectVMs = hydrateTemplateRelatedVMs(filterLinkedVMs(tree.vmByNode[parent.NodeID]))
	}
	parentOtherChildren := parentOtherChildrenList(tree, parent, tpl.NodeID)
	childTemplates := directChildTemplates(tree, tpl.NodeID)
	commitBlockers := buildCommitBlockers(parent != nil, parentDirectVMs, parentOtherChildren, subtreeVMs)

	return &MergePreview{
		Template:       tpl,
		ParentTemplate: parent,
		IsIncremental:  hasBacking,
		Flatten: MergeFlattenPreview{
			Can:        len(flattenBlockers) == 0,
			Blockers:   flattenBlockers,
			SubtreeVMs: subtreeVMs,
		},
		CommitToParent: MergeCommitPreview{
			Can:                 len(commitBlockers) == 0,
			Blockers:            commitBlockers,
			ParentDirectVMs:     parentDirectVMs,
			ParentOtherChildren: parentOtherChildren,
			ChildTemplates:      childTemplates,
			SubtreeVMs:          subtreeVMs,
		},
	}, nil
}
