package pcsupload

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsfunctions"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/checksum"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/taskframework"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsverbose"
	"github.com/qjfoidnh/BaiduPCS-Go/requester/rio"
	"github.com/qjfoidnh/BaiduPCS-Go/requester/uploader"
)

type (
	// StepUpload 上传步骤
	StepUpload int

	// UploadTaskUnit 上传的任务单元
	UploadTaskUnit struct {
		LocalFileChecksum *checksum.LocalFileChecksum // 要上传的本地文件详情
		Step              StepUpload
		SavePath          string // 保存路径
		PrintFormat       string

		PCS               *baidupcs.BaiduPCS
		UploadingDatabase *UploadingDatabase // 数据库
		Parallel          int
		NoRapidUpload     bool   // 禁用秒传
		NoSplitFile       bool   // 禁用分片上传
		Policy            string // 上传重名文件策略

		UploadStatistic *UploadStatistic

		taskInfo *taskframework.TaskInfo
		printer  func(string, ...interface{})
		panDir   string
		panFile  string
		state    *uploader.InstanceState
	}
)

const (
	// StepUploadInit 初始化步骤
	StepUploadInit StepUpload = iota
	// StepUploadRapidUpload 秒传步骤
	StepUploadRapidUpload
	// StepUploadUpload 正常上传步骤
	StepUploadUpload
)

const (
	StrUploadFailed    = "上传文件失败"
	DefaultPrintFormat = "↑ %s/%s %s/s in %s ............"
)

func (utu *UploadTaskUnit) SetTaskInfo(taskInfo *taskframework.TaskInfo) {
	utu.taskInfo = taskInfo
}

func (utu *UploadTaskUnit) SetPrinter(f func(string, ...interface{})) {
	utu.printer = f
}

func (utu *UploadTaskUnit) Printf(format string, a ...interface{}) {
	if pcsverbose.IsVerbose || utu.printer == nil {
		// 调试情况下有其他输出干扰
		fmt.Printf(format, a...)
	} else {
		utu.printer(format, a...)
	}
}

// prepareFile 解析文件阶段
func (utu *UploadTaskUnit) prepareFile() {
	// 解析文件保存路径
	var (
		panDir, panFile = path.Split(utu.SavePath)
	)
	utu.panDir = path.Clean(panDir)
	utu.panFile = panFile

	// 检测断点续传
	utu.state = utu.UploadingDatabase.Search(&utu.LocalFileChecksum.LocalFileMeta)
	if utu.state != nil || utu.LocalFileChecksum.LocalFileMeta.MD5 != nil { // 读取到了md5
		utu.Step = StepUploadUpload
		return
	}

	if utu.NoRapidUpload {
		utu.Step = StepUploadUpload
		return
	}

	if utu.LocalFileChecksum.Length > baidupcs.MaxRapidUploadSize {
		utu.Printf("文件超过20GB, 无法使用秒传功能, 跳过秒传...")
		utu.Step = StepUploadUpload
		return
	}
	// 下一步: 秒传
	utu.Step = StepUploadRapidUpload
}

// rapidUpload 执行秒传
func (utu *UploadTaskUnit) rapidUpload() (isContinue bool, result *taskframework.TaskUnitRunResult) {
	utu.Step = StepUploadRapidUpload

	// TODO: 建立一个通过百度错误码判断重试的函数
	result = &taskframework.TaskUnitRunResult{}

	fdl, pcsError := utu.PCS.CacheFilesDirectoriesList(utu.panDir, baidupcs.DefaultOrderOptions)
	if pcsError != nil {
		switch pcsError.GetErrType() {
		case pcserror.ErrTypeRemoteError:
			switch pcsError.GetRemoteErrCode() {
			case 31066:
			// file does not exist
			// 不缓存文件夹
			default:
				// 其他百度服务器错误, 不重试
				result.ResultMessage = "获取文件列表错误"
				result.Err = pcsError
				return
			}
		default:
			// 未知错误, 重试
			result.ResultMessage = "获取文件列表错误"
			result.NeedRetry = true
			result.Err = pcsError
			return
		}
	}

	// 文件大于128MB, 输出提示信息
	if utu.LocalFileChecksum.Length >= 128*converter.MB {
		utu.Printf("检测秒传中, 请稍候...")
	}

	// 经测试, 文件的 crc32 值并非秒传文件所必需
	err := utu.LocalFileChecksum.Sum(checksum.CHECKSUM_MD5 | checksum.CHECKSUM_SLICE_MD5)
	if err != nil {
		// 不重试
		result.ResultMessage = "计算文件秒传信息错误"
		result.Err = err
		return
	}

	// 检测缓存, 通过文件的md5值判断本地文件和网盘文件是否一样
	if fdl != nil {
		for _, fd := range fdl {
			if fd.Filename == utu.panFile {
				// TODO: fd.MD5 有可能是错误的
				decodedMD5, _ := hex.DecodeString(fd.MD5)
				if bytes.Compare(decodedMD5, utu.LocalFileChecksum.MD5) == 0 {
					utu.Printf("目标文件, %s, 已存在, 跳过...", utu.SavePath)
					result.Succeed = true // 成功
					return
				}
			}
		}
	}

	pcsError = utu.PCS.RapidUpload(utu.SavePath, hex.EncodeToString(utu.LocalFileChecksum.MD5), hex.EncodeToString(utu.LocalFileChecksum.SliceMD5), fmt.Sprint(utu.LocalFileChecksum.CRC32), utu.LocalFileChecksum.Length)
	if pcsError == nil {
		utu.Printf("秒传成功, 保存到网盘路径: %s", utu.SavePath)
		// 统计
		utu.UploadStatistic.AddTotalSize(utu.LocalFileChecksum.Length)
		result.Succeed = true // 成功
		return
	}

	// 判断配额是否已满
	switch pcsError.GetErrType() {
	// 远程服务器错误
	case pcserror.ErrTypeRemoteError:
		switch pcsError.GetRemoteErrCode() {
		case 31112: //exceed quota
			result.ResultMessage = "秒传失败, 超出配额, 网盘容量已满"
			return
		}
	}

	utu.Printf("秒传失败, 开始上传文件...")

	// 保存秒传信息
	utu.UploadingDatabase.UpdateUploading(&utu.LocalFileChecksum.LocalFileMeta, nil)
	utu.UploadingDatabase.Save()
	isContinue = true
	return
}

// upload 上传文件
func (utu *UploadTaskUnit) upload() (result *taskframework.TaskUnitRunResult) {
	utu.Step = StepUploadUpload

	var blockSize int64
	if utu.NoSplitFile {
		// 不分片上传
		blockSize = utu.LocalFileChecksum.Length
	} else {
		blockSize = getBlockSize(utu.LocalFileChecksum.Length)
	}

	muer := uploader.NewMultiUploader(NewPCSUpload(utu.PCS, utu.SavePath), rio.NewFileReaderAtLen64(utu.LocalFileChecksum.GetFile()), &uploader.MultiUploaderConfig{
		Parallel:  utu.Parallel,
		BlockSize: blockSize,
		MaxRate:   pcsconfig.Config.MaxUploadRate,
		Policy:    utu.Policy,
	})

	// 设置断点续传
	if utu.state != nil {
		muer.SetInstanceState(utu.state)
	}
	muer.OnUploadStatusEvent(func(status uploader.Status, updateChan <-chan struct{}) {
		select {
		case <-updateChan:
			utu.UploadingDatabase.UpdateUploading(&utu.LocalFileChecksum.LocalFileMeta, muer.InstanceState())
			utu.UploadingDatabase.Save()
		default:
		}

		utu.Printf(utu.PrintFormat,
			converter.ConvertFileSize(status.Uploaded(), 2),
			converter.ConvertFileSize(status.TotalSize(), 2),
			converter.ConvertFileSize(status.SpeedsPerSecond(), 2),
			status.TimeElapsed(),
		)
	})

	// result
	result = &taskframework.TaskUnitRunResult{}
	muer.OnSuccess(func() {
		utu.Printf("上传文件成功, 保存到网盘路径: %s", utu.SavePath)
		// 统计
		utu.UploadStatistic.AddTotalSize(utu.LocalFileChecksum.Length)
		utu.UploadingDatabase.Delete(&utu.LocalFileChecksum.LocalFileMeta) // 删除
		utu.UploadingDatabase.Save()
		result.Succeed = true
	})
	muer.OnError(func(err error) {
		pcsError, ok := err.(pcserror.Error)
		if !ok {
			// 未知错误类型 (非预期的)
			// 不重试
			result.ResultMessage = "上传文件错误"
			result.Err = err
			return
		}

		// 默认需要重试
		result.NeedRetry = true

		switch pcsError.GetErrType() {
		case pcserror.ErrTypeRemoteError:
			// 远程百度服务器的错误
			switch pcsError.GetRemoteErrCode() {
			case 31363:
				// block miss in superfile2, 上传状态过期
				// 需要重试的
				utu.UploadingDatabase.Delete(&utu.LocalFileChecksum.LocalFileMeta)
				utu.UploadingDatabase.Save()

				result.ResultMessage = StrUploadFailed
				result.Err = errors.New("上传状态过期, 重新上传")
			case 31061:
				// 已存在重名文件, 不重试
				result.ResultMessage = StrUploadFailed
				result.Err = pcsError
				result.NeedRetry = false
				return
			default:
				result.ResultMessage = StrUploadFailed
				result.Err = pcsError
			}
		case pcserror.ErrTypeNetError:
			// 网络错误
			result.ResultMessage = StrUploadFailed
			result.Err = pcsError
			if strings.Contains(pcsError.GetError().Error(), "413 Request Entity Too Large") {
				// 请求实体过大
				// 不重试
				result.NeedRetry = false
				return
			}
		default:
			result.ResultMessage = StrUploadFailed
			result.NeedRetry = false
			result.Err = pcsError
		}
		return
	})
	muer.Execute()

	return
}

func (utu *UploadTaskUnit) OnRetry(lastRunResult *taskframework.TaskUnitRunResult) {
	// 输出错误信息
	if lastRunResult.Err == nil {
		// result中不包含Err, 忽略输出
		utu.Printf("%s, 重试 %d/%d", lastRunResult.ResultMessage, utu.taskInfo.Retry(), utu.taskInfo.MaxRetry())
		return
	}
	utu.Printf("%s, %s, 重试 %d/%d", lastRunResult.ResultMessage, lastRunResult.Err, utu.taskInfo.Retry(), utu.taskInfo.MaxRetry())
}

func (utu *UploadTaskUnit) OnSuccess(lastRunResult *taskframework.TaskUnitRunResult) {
}

func (utu *UploadTaskUnit) OnFailed(lastRunResult *taskframework.TaskUnitRunResult) {
	// 失败
	if lastRunResult.Err == nil {
		// result中不包含Err, 忽略输出
		utu.Printf("%s", lastRunResult.ResultMessage)
		return
	}
	utu.Printf("%s, %s", lastRunResult.ResultMessage, lastRunResult.Err)
}

func (utu *UploadTaskUnit) OnComplete(lastRunResult *taskframework.TaskUnitRunResult) {
}

func (utu *UploadTaskUnit) RetryWait() time.Duration {
	return pcsfunctions.RetryWait(utu.taskInfo.Retry())
}

func (utu *UploadTaskUnit) Run() (result *taskframework.TaskUnitRunResult) {
	utu.Printf("准备上传: %s", utu.LocalFileChecksum.Path)

	err := utu.LocalFileChecksum.OpenPath()
	if err != nil {
		utu.Printf("文件不可读, 错误信息: %s, 跳过...", err)
		return
	}
	defer utu.LocalFileChecksum.Close() // 关闭文件

	// 准备文件
	utu.prepareFile()

	switch utu.Step {
	case StepUploadRapidUpload:
		goto stepUploadRapidUpload
	case StepUploadUpload:
		goto stepUploadUpload
	}

stepUploadRapidUpload:
	// 秒传
	{
		isContinue, rapidUploadResult := utu.rapidUpload()
		if !isContinue {
			// 不继续, 返回秒传的结果
			return rapidUploadResult
		}
	}

stepUploadUpload:
	// 正常上传流程
	uploadResult := utu.upload()

	return uploadResult
}
