package template

import (
	"errors"

	"kvm_console/model"
)

func ListTemplates() ([]model.LXCTemplate, error) {
	var rows []model.LXCTemplate
	if err := model.DB.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func GetTemplate(name string) (*model.LXCTemplate, error) {
	var tpl model.LXCTemplate
	if err := model.DB.Where("name = ?", name).First(&tpl).Error; err != nil {
		return nil, errors.New("模板不存在")
	}
	return &tpl, nil
}
