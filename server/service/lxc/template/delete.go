package template

import (
	"errors"

	"kvm_console/model"
)

// DeleteTemplate 删除模板：有派生容器则拒绝；否则销毁基底容器 + 删 DB 行。
func DeleteTemplate(name string) error {
	tpl, err := GetTemplate(name)
	if err != nil {
		return err
	}
	// 派生容器检查（模板名匹配 Template 列）
	var cnt int64
	model.DB.Model(&model.LXCCache{}).Where("template = ? AND present = ?", name, true).Count(&cnt)
	if cnt > 0 {
		return errors.New("存在使用该模板的容器，请先删除相关容器")
	}
	// destroyBase 按 backing 分支销毁：zfs → DestroyBase（含 @base 快照）；dir/overlay → lxc-destroy。
	if err := destroyBase(tpl.BaseContainerName, tpl.Backing); err != nil {
		return errors.New("销毁基底容器失败: " + err.Error())
	}
	if err := model.DB.Delete(tpl).Error; err != nil {
		return err
	}
	return nil
}
