package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kvm_console/service"
)

type execLXCReq struct {
	Command    string `json:"command" binding:"required"`
	TimeoutSec int    `json:"timeout_sec"`
}

// PostLXCExec 在容器内执行单条命令，同步返回 stdout/stderr/exit_code。
func PostLXCExec(c *gin.Context) {
	var req execLXCReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	res, err := service.LXCExecContainer(c.Param("name"), req.Command, req.TimeoutSec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": res})
}
