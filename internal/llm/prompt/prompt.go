package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
)

type PromptID string

const (
	PromptCoder      PromptID = "coder"
	PromptTitle      PromptID = "title"
	PromptTask       PromptID = "task"
	PromptSummarizer PromptID = "summarizer"
	PromptDefault    PromptID = "default"
)

func GetPrompt(promptID PromptID, provider string, contextPaths ...string) string {
	basePrompt := ""
	switch promptID {
	case PromptCoder:
		basePrompt = CoderPrompt(provider, contextPaths...)
	case PromptTitle:
		basePrompt = TitlePrompt()
	case PromptTask:
		basePrompt = TaskPrompt()
	case PromptSummarizer:
		basePrompt = SummarizerPrompt()
	default:
		basePrompt = "You are a helpful assistant"
	}
	return basePrompt
}

func getContextFromPaths(workingDir string, contextPaths []string) string {
	return processContextPaths(workingDir, contextPaths)
}

// expandPath expands ~ and environment variables in file paths
func expandPath(path string) string {
	// Handle tilde expansion
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(homeDir, path[2:])
		}
	} else if path == "~" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			path = homeDir
		}
	}

	// Handle environment variable expansion using the same pattern as config
	if strings.HasPrefix(path, "$") {
		resolver := config.NewEnvironmentVariableResolver(env.New())
		if expanded, err := resolver.ResolveValue(path); err == nil {
			path = expanded
		}
	}

	return path
}

func processContextPaths(workDir string, paths []string) string {
	var (
		wg       sync.WaitGroup
		resultCh = make(chan string)
	)

	// Track processed files to avoid duplicates across all inputs and transclusions
	processedFiles := csync.NewMap[string, bool]()

	for _, path := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()

			p = expandPath(p)
			fullPath := p
			if !filepath.IsAbs(p) {
				fullPath = filepath.Join(workDir, p)
			}

			info, err := os.Stat(fullPath)
			if err != nil {
				return
			}

			if info.IsDir() {
				filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !d.IsDir() {
						lowerPath := strings.ToLower(path)
						if alreadyProcessed, _ := processedFiles.Get(lowerPath); !alreadyProcessed {
							processedFiles.Set(lowerPath, true)
							if result := processFileWithTransclusion(path, processedFiles); result != "" {
								resultCh <- result
							}
						}
					}
					return nil
				})
			} else {
				lowerPath := strings.ToLower(fullPath)
				if alreadyProcessed, _ := processedFiles.Get(lowerPath); !alreadyProcessed {
					processedFiles.Set(lowerPath, true)
					result := processFileWithTransclusion(fullPath, processedFiles)
					if result != "" {
						resultCh <- result
					}
				}
			}
		}(path)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make([]string, 0)
	for result := range resultCh {
		results = append(results, result)
	}

	return strings.Join(results, "\n")
}

func processFileWithTransclusion(filePath string, seen *csync.Map[string, bool]) string {
	contentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}

	type chunk struct {
		isInclude bool
		text      string
		ch        chan string
	}

	var header strings.Builder
	header.WriteString("# From:")
	header.WriteString(filePath)
	header.WriteString("\n")

	content := string(contentBytes)
	segments := strings.SplitAfter(content, "\n")
	parent := filepath.Dir(filePath)

	chunks := make([]chunk, 0, len(segments))

	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "@") {
			inc := strings.TrimSpace(strings.TrimSuffix(seg[1:], "\n"))
			if inc == "" {
				chunks = append(chunks, chunk{isInclude: false, text: seg})
				continue
			}
			incPath := inc
			if !filepath.IsAbs(incPath) {
				incPath = filepath.Join(parent, incPath)
			}
			incPath = expandPath(incPath)
			absPath, err := filepath.Abs(incPath)
			if err != nil {
				chunks = append(chunks, chunk{isInclude: false, text: seg})
				continue
			}
			info, err := os.Stat(absPath)
			if err != nil || info.IsDir() {
				chunks = append(chunks, chunk{isInclude: false, text: seg})
				continue
			}
			key := strings.ToLower(absPath)
			if already, _ := seen.Get(key); already {
				// Dedup: skip emitting this include
				continue
			}
			// Mark as seen before launching processing to avoid duplicate work
			seen.Set(key, true)
			ch := make(chan string, 1)
			chunks = append(chunks, chunk{isInclude: true, ch: ch})
			go func(p string, out chan<- string) {
				defer close(out)
				out <- processFileWithTransclusion(p, seen)
			}(absPath, ch)
			continue
		}
		chunks = append(chunks, chunk{isInclude: false, text: seg})
	}

	var b strings.Builder
	b.WriteString(header.String())
	for _, c := range chunks {
		if !c.isInclude {
			b.WriteString(c.text)
			continue
		}
		if c.ch != nil {
			if included, ok := <-c.ch; ok {
				b.WriteString(included)
			}
		}
	}

	return b.String()
}
