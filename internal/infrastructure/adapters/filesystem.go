package adapters

import (
    "errors"
    domconst "multinic-agent/internal/domain/constants"
    "multinic-agent/internal/domain/interfaces"
    "os"
    "path/filepath"
    "strings"
)

// RealFileSystem은 실제 파일 시스템을 사용하는 FileSystem 구현체입니다
type RealFileSystem struct{}

// NewRealFileSystem은 새로운 RealFileSystem을 생성합니다
func NewRealFileSystem() interfaces.FileSystem {
    return &RealFileSystem{}
}

// isSafeConfigPath validates absolute paths targeting known config roots and rejects traversal.
func isSafeConfigPath(p string) bool {
    if p == "" { return false }
    if !filepath.IsAbs(p) { return false }
    clean := filepath.Clean(p)
    if strings.Contains(clean, "..") { return false }
    // allowlist of config roots we manage
    allowed := []string{
        domconst.NetplanConfigDir,
        domconst.RHELNetworkScriptsDir,
        domconst.NetworkManagerDir,
        domconst.DefaultBackupDir,
    }
    for _, base := range allowed {
        if strings.HasPrefix(clean, base) {
            return true
        }
    }
    return false
}

// ReadFile은 파일을 읽습니다
func (fs *RealFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile은 파일에 데이터를 씁니다
func (fs *RealFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
    if !isSafeConfigPath(path) {
        return errors.New("unsafe path rejected")
    }
    // 디렉토리가 없으면 생성
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return err
    }

    return os.WriteFile(path, data, perm)
}

// Exists는 파일이나 디렉토리가 존재하는지 확인합니다
func (fs *RealFileSystem) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// MkdirAll은 디렉토리를 재귀적으로 생성합니다
func (fs *RealFileSystem) MkdirAll(path string, perm os.FileMode) error {
    if !isSafeConfigPath(path) {
        return errors.New("unsafe path rejected")
    }
    return os.MkdirAll(path, perm)
}

// Remove는 파일이나 디렉토리를 삭제합니다
func (fs *RealFileSystem) Remove(path string) error {
    if !isSafeConfigPath(path) {
        return errors.New("unsafe path rejected")
    }
    return os.Remove(path)
}

// ListFiles는 디렉토리의 파일 목록을 반환합니다
func (fs *RealFileSystem) ListFiles(path string) ([]string, error) {
    entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}
