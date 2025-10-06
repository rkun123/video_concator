package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// VideoInfo は動画ファイルの情報を格納する構造体
type VideoInfo struct {
	Path    string
	ModTime time.Time
}

func main() {
	// コマンドライン引数を定義
	inputDir := flag.String("dir", "", "動画ファイルが含まれるディレクトリ (必須)")
	outputFile := flag.String("output", "", "出力ファイル名 (必須)")
	resolution := flag.String("resolution", "1920x1080", "解像度 (例: 1920x1080)")
	framerate := flag.Int("framerate", 60, "フレームレート")
	encoder := flag.String("encoder", "", "ビデオエンコーダー (デフォルトはOSに応じて自動選択)")
	flag.Parse()

	// 必須引数のチェック
	if *inputDir == "" || *outputFile == "" {
		fmt.Println("エラー: -dir と -output は必須です。")
		flag.Usage()
		os.Exit(1)
	}

	// ffmpegコマンドの存在を確認
	if !isFFmpegAvailable() {
		log.Fatal("エラー: ffmpegが見つかりません。ffmpegをインストールし、PATHに追加してください。")
	}

	// 1. ディレクトリ内の動画ファイルを検索し、日付順にソート
	log.Println("動画ファイルを検索中...")
	videoFiles, err := findAndSortVideos(*inputDir)
	if err != nil {
		log.Fatalf("動画ファイルの検索に失敗しました: %v", err)
	}
	if len(videoFiles) == 0 {
		log.Fatalf("ディレクトリ '%s' に動画ファイルが見つかりませんでした。", *inputDir)
	}
	log.Printf("%d個の動画ファイルが見つかりました。\n", len(videoFiles))

	// 2. ffmpegのconcat demuxer用のリストファイルを作成
	listFilePath, err := createConcatListFile(videoFiles)
	if err != nil {
		log.Fatalf("結合リストファイルの作成に失敗しました: %v", err)
	}
	// プログラム終了時にリストファイルを削除
	defer os.Remove(listFilePath)

	// 3. エンコーダーを決定
	chosenEncoder := *encoder
	if chosenEncoder == "" {
		chosenEncoder = getDefaultEncoder()
	}
	log.Printf("使用するエンコーダー: %s\n", chosenEncoder)

	// 4. ffmpegコマンドを組み立てて実行
	log.Println("動画の結合とエンコードを開始します...")
	cmd := exec.Command(
		"ffmpeg",
		"-f", "concat", // concat demuxerを使用
		"-safe", "0", // 絶対パスを許可
		"-i", listFilePath, // 入力リストファイル
		"-vf", fmt.Sprintf("scale=%s,fps=%d", *resolution, *framerate), // 解像度とフレームレートを設定
		"-c:v", chosenEncoder, // ビデオエンコーダー
		"-c:a", "aac", // 音声コーデック（再エンコード）
		"-b:a", "192k", // 音声ビットレート
		"-y", // 出力ファイルを上書き
		*outputFile,
	)

	// ffmpegの標準出力と標準エラー出力をコンソールに表示
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		log.Fatalf("ffmpegの実行に失敗しました: %v", err)
	}

	log.Printf("処理が完了しました。出力ファイル: %s\n", *outputFile)
}

// isFFmpegAvailable はffmpegコマンドが利用可能かを確認する
func isFFmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// findAndSortVideos は指定されたディレクトリ内の動画ファイルを検索し、更新日時順にソートする
func findAndSortVideos(dir string) ([]string, error) {
	var videos []VideoInfo
	supportedExtensions := map[string]bool{
		".mp4": true,
		".mov": true,
		".mkv": true,
		".avi": true,
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			if supportedExtensions[ext] {
				videos = append(videos, VideoInfo{Path: path, ModTime: info.ModTime()})
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// ModTime（更新日時）でソート
	sort.Slice(videos, func(i, j int) bool {
		return videos[i].ModTime.Before(videos[j].ModTime)
	})

	var sortedPaths []string
	for _, v := range videos {
		absPath, err := filepath.Abs(v.Path)
		if err != nil {
			return nil, fmt.Errorf("絶対パスの取得に失敗しました: %s, %v", v.Path, err)
		}
		sortedPaths = append(sortedPaths, absPath)
	}

	return sortedPaths, nil
}

// createConcatListFile はffmpegのconcat demuxerが読み込むための一時的なリストファイルを作成する
func createConcatListFile(files []string) (string, error) {
	tempFile, err := os.CreateTemp("", "concat-list-*.txt")
	if err != nil {
		return "", err
	}
	defer tempFile.Close()

	writer := bufio.NewWriter(tempFile)
	for _, file := range files {
		// パスに含まれるシングルクォートをエスケープ
		escapedPath := strings.ReplaceAll(file, "'", "'\\''")
		// file 'path' というフォーマットで書き込む
		_, err := writer.WriteString(fmt.Sprintf("file '%s'\n", escapedPath))
		if err != nil {
			return "", err
		}
	}
	writer.Flush()
	return tempFile.Name(), nil
}

// getDefaultEncoder は実行中のOSに基づいてデフォルトのエンコーダーを返す
func getDefaultEncoder() string {
	switch runtime.GOOS {
	case "windows":
		// NVIDIA GPUが存在するかどうかを簡易的にチェックすることも可能だが、
		// まずはhevc_nvencを試し、失敗したらffmpegがエラーを返すというアプローチがシンプル。
		return "hevc_nvenc"
	case "darwin": // macOS
		return "hevc_videotoolbox"
	default: // Linuxなど
		return "libx265"
	}
}
