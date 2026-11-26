package cmd

import (
	"log"
	"path/filepath"
	"sync"

	. "github.com/Ackites/KillWxapkg/internal/cmd"
	. "github.com/Ackites/KillWxapkg/internal/config"
	"github.com/Ackites/KillWxapkg/internal/progress"
	"github.com/Ackites/KillWxapkg/internal/restore"
)

func Execute(appID, input, outputDir, fileExt string, restoreDir bool, pretty bool, noClean bool, save bool, sensitive bool) {
	// 存储配置
	configManager := NewSharedConfigManager()
	configManager.Set("appID", appID)
	configManager.Set("input", input)
	configManager.Set("outputDir", outputDir)
	configManager.Set("fileExt", fileExt)
	configManager.Set("restoreDir", restoreDir)
	configManager.Set("pretty", pretty)
	configManager.Set("noClean", noClean)
	configManager.Set("save", save)
	configManager.Set("sensitive", sensitive)

	inputFiles := ParseInput(input, fileExt)

	if len(inputFiles) == 0 {
		log.Println("未找到任何文件")
		return
	}

	// 确定输出目录
	if outputDir == "" {
		outputDir = DetermineOutputDir(input, appID)
	}

	progress.SetPackages(len(inputFiles))

	var wg sync.WaitGroup
	for _, inputFile := range inputFiles {
		wg.Add(1)
		go func(file string) {
			defer wg.Done()
			err := ProcessFile(file, outputDir, appID, save)
			if err != nil {
				log.Printf("处理文件 %s 时出错: %v\n", file, err)
				progress.Emit("package_error", map[string]interface{}{
					"file": filepath.Base(file), "error": err.Error()})
			} else {
				log.Printf("成功处理文件: %s\n", file)
			}
			progress.PackageDone()
		}(inputFile)
	}
	wg.Wait()

	// 还原工程目录结构
	progress.Emit("restore_start", map[string]interface{}{
		"restore_dir": restoreDir})
	restore.ProjectStructure(outputDir, restoreDir)
	done, total := progress.Snapshot()
	progress.Emit("pipeline_done", map[string]interface{}{
		"done_packages": done, "total_packages": total})
}
