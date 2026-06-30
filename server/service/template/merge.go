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
