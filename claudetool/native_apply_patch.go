package claudetool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
)

const NativeOpenAIApplyPatchDescription = `Apply file changes using OpenAI Responses native apply_patch operations.
Each call contains one create_file, update_file, or delete_file operation and a path. Create and update operations also contain a headerless V4A diff.
For create_file, every diff line starts with "+". For update_file, sections use "@@" headers followed by lines prefixed with exactly one " " (context), "-" (remove), or "+" (add). An initial section may omit "@@". "@@ text" anchors after a matching source line; stacked anchors narrow nested scopes. "*** End of File" anchors a section at EOF. Insertion-only sections are supported.
Sections match sequentially from the prior section. Context may match exactly, ignoring trailing whitespace, or ignoring surrounding whitespace. The file's existing trailing newline is preserved.`

type nativeOpenAIApplyPatchOperation struct {
	Type string  `json:"type"`
	Path string  `json:"path"`
	Diff *string `json:"diff,omitempty"`
}

func (p *PatchTool) nativeOpenAIApplyPatchTool() *llm.Tool {
	return &llm.Tool{
		Name:        ApplyPatchName,
		Type:        ApplyPatchName,
		Description: NativeOpenAIApplyPatchDescription,
		Sequential:  true,
		Run: llm.RunJSON(func(ctx context.Context, operation nativeOpenAIApplyPatchOperation) llm.ToolOut {
			return p.runNativeOpenAIApplyPatch(ctx, operation)
		}),
	}
}

func (p *PatchTool) runNativeOpenAIApplyPatch(ctx context.Context, operation nativeOpenAIApplyPatchOperation) llm.ToolOut {
	if err := ctx.Err(); err != nil {
		return llm.ErrorToolOut(err)
	}
	if strings.TrimSpace(operation.Path) == "" {
		err := fmt.Errorf("apply_patch path is required")
		p.logResult(ctx, "invalid_input", err)
		return llm.ErrorToolOut(err)
	}
	if operation.Type != "create_file" && operation.Type != "update_file" && operation.Type != "delete_file" {
		err := fmt.Errorf("unrecognized apply_patch operation %q", operation.Type)
		p.logResult(ctx, "invalid_input", err)
		return llm.ErrorToolOut(err)
	}
	if operation.Type != "delete_file" && operation.Diff == nil {
		err := fmt.Errorf("%s operation requires a diff", operation.Type)
		p.logResult(ctx, "invalid_input", err)
		return llm.ErrorToolOut(err)
	}
	if operation.Type == "delete_file" && operation.Diff != nil {
		err := fmt.Errorf("delete_file operation must not include a diff")
		p.logResult(ctx, "invalid_input", err)
		return llm.ErrorToolOut(err)
	}

	path := operation.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.getWorkingDir(), path)
	}
	path = filepath.Clean(path)
	unlock := p.lockPath(path)
	defer unlock()

	var oldContent, newContent string
	var mode os.FileMode = 0o600
	var resultVerb string
	var err error
	switch operation.Type {
	case "create_file":
		resultVerb = "Created"
		newContent, err = applyDiff("", *operation.Diff, true)
		var createdDirs []string
		if err == nil {
			createdDirs, err = makePatchParentDirs(path)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = createPatchFileAtomic(ctx, path, newContent, mode)
			if errors.Is(err, os.ErrExist) {
				err = fmt.Errorf("file %q already exists", operation.Path)
			}
		}
		if err != nil {
			removeEmptyPatchDirs(createdDirs)
		}
	case "update_file":
		resultVerb = "Updated"
		var old []byte
		old, err = os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("file %q does not exist", operation.Path)
		} else if err != nil {
			err = fmt.Errorf("failed to read file %q: %w", operation.Path, err)
		}
		if err == nil {
			oldContent = string(old)
			newContent, err = applyDiff(oldContent, *operation.Diff, false)
		}
		if err == nil {
			if info, statErr := os.Stat(path); statErr == nil {
				mode = info.Mode().Perm()
			} else {
				err = statErr
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			writePath := path
			if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
				writePath = resolved
			}
			err = checkPatchFileWritable(writePath)
			if err == nil {
				err = writePatchFileAtomic(ctx, writePath, newContent, mode)
			}
		}
	case "delete_file":
		resultVerb = "Deleted"
		var info os.FileInfo
		info, err = os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("file %q does not exist", operation.Path)
		} else if err != nil {
			err = fmt.Errorf("inspect file %q: %w", operation.Path, err)
		}
		if err == nil {
			if info.IsDir() {
				err = fmt.Errorf("path %q is a directory, not a file", operation.Path)
			} else if info.Mode().IsRegular() {
				if old, readErr := os.ReadFile(path); readErr == nil {
					oldContent = string(old)
				}
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = os.Remove(path)
		}
	}
	if err != nil {
		p.logResult(ctx, classifyPatchError(err), err)
		return llm.ErrorToolOut(err)
	}

	p.logResult(ctx, "success", nil)
	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("%s %s", resultVerb, operation.Path)),
		Display: PatchDisplayData{
			Path: path,
			Diff: generateUnifiedDiff(path, oldContent, newContent),
		},
	}
}

func makePatchParentDirs(path string) ([]string, error) {
	var created []string
	var makeDir func(string) error
	makeDir = func(dir string) error {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", dir)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent != dir {
			if err := makeDir(parent); err != nil {
				return err
			}
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
				return nil
			}
			return err
		}
		created = append(created, dir)
		return nil
	}
	if err := makeDir(filepath.Dir(path)); err != nil {
		removeEmptyPatchDirs(created)
		return nil, err
	}
	return created, nil
}

func removeEmptyPatchDirs(dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}

func createPatchFileAtomic(ctx context.Context, path, content string, mode os.FileMode) (err error) {
	tempPath, err := stagePatchFile(path, content, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Link(tempPath, path)
}

func checkPatchFileWritable(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open file %q for writing: %w", path, err)
	}
	return file.Close()
}

func writePatchFileAtomic(ctx context.Context, path, content string, mode os.FileMode) (err error) {
	tempPath, err := stagePatchFile(path, content, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func stagePatchFile(path, content string, mode os.FileMode) (tempPath string, err error) {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath = file.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = file.Chmod(mode); err == nil {
		_, err = file.WriteString(content)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return tempPath, nil
}
