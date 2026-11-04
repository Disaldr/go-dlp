package script

import (
	"embed"
	"io/fs"
)

// Встроенные скрипты: каталог scripts/ (*.js и VERSION). Из него же
// собирается публикуемый пакет (cmd/go-dlp-pack), поэтому версия
// встроенного набора совпадает с версией пакета на момент сборки, и
// пакет из сети ставится, только если он новее.
//
//go:embed scripts
var builtinFS embed.FS

// Builtin компилирует встроенные скрипты.
func Builtin() (*Pack, error) {
	sub, err := fs.Sub(builtinFS, "scripts")
	if err != nil {
		return nil, err
	}
	return LoadDir(sub)
}
