package claudetool

import (
	"fmt"
	"strings"
	"unicode"
)

// applyDiff applies the headerless V4A diff used by OpenAI's native
// apply_patch tool. Create diffs contain only '+' lines; update diffs contain
// sections made of anchors and context/add/delete lines.
func applyDiff(input, diff string, create bool) (string, error) {
	diffLines := normalizeDiffLines(diff)
	if create {
		return parseCreateDiff(diffLines)
	}
	chunks, err := parseUpdateDiff(diffLines, input)
	if err != nil {
		return "", err
	}
	return applyDiffChunks(input, chunks)
}

type applyDiffChunk struct {
	origIndex int
	delLines  []string
	insLines  []string
}

type applyDiffParser struct {
	lines []string
	index int
	fuzz  int
}

const (
	applyDiffEndPatch = "*** End Patch"
	applyDiffEndFile  = "*** End of File"
)

var (
	applyDiffEndSectionMarkers = []string{
		applyDiffEndPatch,
		"*** Update File:",
		"*** Delete File:",
		"*** Add File:",
		applyDiffEndFile,
	}
	applyDiffSectionTerminators = []string{
		applyDiffEndPatch,
		"*** Update File:",
		"*** Delete File:",
		"*** Add File:",
	}
)

func normalizeDiffLines(diff string) []string {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func applyDiffDone(parser *applyDiffParser, prefixes []string) bool {
	if parser.index >= len(parser.lines) {
		return true
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(parser.lines[parser.index], prefix) {
			return true
		}
	}
	return false
}

func applyDiffReadString(parser *applyDiffParser, prefix string) string {
	if parser.index < len(parser.lines) && strings.HasPrefix(parser.lines[parser.index], prefix) {
		value := strings.TrimPrefix(parser.lines[parser.index], prefix)
		parser.index++
		return value
	}
	return ""
}

func parseCreateDiff(lines []string) (string, error) {
	parser := applyDiffParser{lines: append(append([]string(nil), lines...), applyDiffEndPatch)}
	var output []string
	for !applyDiffDone(&parser, applyDiffSectionTerminators) {
		line := parser.lines[parser.index]
		parser.index++
		if !strings.HasPrefix(line, "+") {
			return "", fmt.Errorf("Invalid Add File Line: %s", line)
		}
		output = append(output, strings.TrimPrefix(line, "+"))
	}
	return strings.Join(output, "\n"), nil
}

func parseUpdateDiff(lines []string, input string) ([]applyDiffChunk, error) {
	parser := applyDiffParser{lines: append(append([]string(nil), lines...), applyDiffEndPatch)}
	inputLines := strings.Split(input, "\n")
	var chunks []applyDiffChunk
	cursor := 0

	for !applyDiffDone(&parser, applyDiffEndSectionMarkers) {
		anchors, anchorCount := readDiffAnchors(&parser)
		if anchorCount == 0 && cursor != 0 {
			return nil, fmt.Errorf("Invalid Line:\n%s", parser.lines[parser.index])
		}

		requireAnchorMatch := anchorCount > 1
		for i, anchor := range anchors {
			var err error
			cursor, err = advanceDiffCursorToAnchor(anchor, inputLines, cursor, &parser, requireAnchorMatch, i > 0)
			if err != nil {
				return nil, err
			}
		}

		context, sectionChunks, endIndex, eof, err := readDiffSection(parser.lines, parser.index)
		if err != nil {
			return nil, err
		}
		newIndex, fuzz := findDiffContext(inputLines, context, cursor, eof)
		if newIndex == -1 {
			if eof {
				return nil, fmt.Errorf("Invalid EOF Context %d:\n%s", cursor, strings.Join(context, "\n"))
			}
			return nil, fmt.Errorf("Invalid Context %d:\n%s", cursor, strings.Join(context, "\n"))
		}

		parser.fuzz += fuzz
		for _, chunk := range sectionChunks {
			chunk.origIndex += newIndex
			chunks = append(chunks, chunk)
		}
		cursor = newIndex + len(context)
		parser.index = endIndex
	}
	return chunks, nil
}

func readDiffAnchors(parser *applyDiffParser) ([]string, int) {
	var anchors []string
	anchorCount := 0
	for {
		start := parser.index
		anchor := applyDiffReadString(parser, "@@ ")
		consumed := parser.index != start
		if !consumed && parser.index < len(parser.lines) && parser.lines[parser.index] == "@@" {
			parser.index++
			consumed = true
		}
		if !consumed {
			break
		}
		anchorCount++
		if strings.TrimSpace(anchor) != "" {
			anchors = append(anchors, anchor)
		}
	}
	return anchors, anchorCount
}

func advanceDiffCursorToAnchor(anchor string, inputLines []string, cursor int, parser *applyDiffParser, requireMatch, forceForwardSearch bool) (int, error) {
	found := false
	if !forceForwardSearch {
		for _, line := range inputLines[:cursor] {
			if line == anchor {
				found = true
				break
			}
		}
	}
	if !found {
		for i := cursor; i < len(inputLines); i++ {
			if inputLines[i] == anchor {
				cursor = i + 1
				found = true
				break
			}
		}
	}

	if !found {
		if !forceForwardSearch {
			for _, line := range inputLines[:cursor] {
				if strings.TrimSpace(line) == strings.TrimSpace(anchor) {
					found = true
					break
				}
			}
		}
		if !found {
			for i := cursor; i < len(inputLines); i++ {
				if strings.TrimSpace(inputLines[i]) == strings.TrimSpace(anchor) {
					cursor = i + 1
					parser.fuzz++
					found = true
					break
				}
			}
		}
	}

	if requireMatch && !found {
		return 0, fmt.Errorf("Invalid Anchor %d:\n%s", cursor, anchor)
	}
	return cursor, nil
}

func readDiffSection(lines []string, startIndex int) (context []string, chunks []applyDiffChunk, endIndex int, eof bool, err error) {
	var delLines, insLines []string
	mode := byte(' ')
	index := startIndex
	origIndex := index

	for index < len(lines) {
		raw := lines[index]
		if strings.HasPrefix(raw, "@@") ||
			strings.HasPrefix(raw, applyDiffEndPatch) ||
			strings.HasPrefix(raw, "*** Update File:") ||
			strings.HasPrefix(raw, "*** Delete File:") ||
			strings.HasPrefix(raw, "*** Add File:") ||
			strings.HasPrefix(raw, applyDiffEndFile) {
			break
		}
		if raw == "***" {
			break
		}
		if strings.HasPrefix(raw, "***") {
			return nil, nil, 0, false, fmt.Errorf("Invalid Line: %s", raw)
		}

		index++
		lastMode := mode
		line := raw
		if line == "" {
			line = " "
		}
		switch line[0] {
		case '+', '-', ' ':
			mode = line[0]
		default:
			return nil, nil, 0, false, fmt.Errorf("Invalid Line: %s", line)
		}
		line = line[1:]

		if mode == ' ' && lastMode != mode && (len(insLines) > 0 || len(delLines) > 0) {
			chunks = append(chunks, applyDiffChunk{
				origIndex: len(context) - len(delLines),
				delLines:  delLines,
				insLines:  insLines,
			})
			delLines = nil
			insLines = nil
		}

		switch mode {
		case '-':
			delLines = append(delLines, line)
			context = append(context, line)
		case '+':
			insLines = append(insLines, line)
		case ' ':
			context = append(context, line)
		}
	}

	if len(insLines) > 0 || len(delLines) > 0 {
		chunks = append(chunks, applyDiffChunk{
			origIndex: len(context) - len(delLines),
			delLines:  delLines,
			insLines:  insLines,
		})
	}
	if index < len(lines) && lines[index] == applyDiffEndFile {
		return context, chunks, index + 1, true, nil
	}
	if index == origIndex {
		return nil, nil, 0, false, fmt.Errorf("Nothing in this section - index=%d %s", index, lines[index])
	}
	return context, chunks, index, false, nil
}

func findDiffContext(lines, context []string, start int, eof bool) (int, int) {
	if eof {
		searchLines := lines
		if len(searchLines) > 0 && searchLines[len(searchLines)-1] == "" {
			searchLines = searchLines[:len(searchLines)-1]
		}
		endStart := max(0, len(searchLines)-len(context))
		if index, fuzz := findDiffContextCore(searchLines, context, endStart); index != -1 {
			return index, fuzz
		}
		index, fuzz := findDiffContextCore(searchLines, context, min(start, len(searchLines)))
		return index, fuzz + 10000
	}
	return findDiffContextCore(lines, context, start)
}

func findDiffContextCore(lines, context []string, start int) (int, int) {
	if len(context) == 0 {
		return start, 0
	}
	mappers := []struct {
		mapLine func(string) string
		fuzz    int
	}{
		{mapLine: func(line string) string { return line }},
		{mapLine: func(line string) string { return strings.TrimRightFunc(line, unicode.IsSpace) }, fuzz: 1},
		{mapLine: strings.TrimSpace, fuzz: 100},
	}
	for _, mapper := range mappers {
		for i := start; i < len(lines); i++ {
			if diffLinesEqual(lines, context, i, mapper.mapLine) {
				return i, mapper.fuzz
			}
		}
	}
	return -1, 0
}

func diffLinesEqual(source, target []string, start int, mapLine func(string) string) bool {
	if start+len(target) > len(source) {
		return false
	}
	for i := range target {
		if mapLine(source[start+i]) != mapLine(target[i]) {
			return false
		}
	}
	return true
}

func applyDiffChunks(input string, chunks []applyDiffChunk) (string, error) {
	origLines := strings.Split(input, "\n")
	var destLines []string
	origIndex := 0
	for _, chunk := range chunks {
		if chunk.origIndex > len(origLines) {
			return "", fmt.Errorf("applyDiff: chunk.origIndex %d > input length %d", chunk.origIndex, len(origLines))
		}
		if origIndex > chunk.origIndex {
			return "", fmt.Errorf("applyDiff: overlapping chunk at %d (cursor %d)", chunk.origIndex, origIndex)
		}
		destLines = append(destLines, origLines[origIndex:chunk.origIndex]...)
		origIndex = chunk.origIndex
		destLines = append(destLines, chunk.insLines...)
		origIndex += len(chunk.delLines)
	}
	destLines = append(destLines, origLines[origIndex:]...)
	return strings.Join(destLines, "\n"), nil
}
