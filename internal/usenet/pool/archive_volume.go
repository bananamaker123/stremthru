package usenet_pool

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

type archiveVolume struct {
	n    int
	name string
}

type archiveVolumeGroup[T any] struct {
	BaseName  string   // e.g., "video" for video.part01.rar, video.part02.rar
	Aliased   bool     // no standard archive extension
	FileType  FileType // RAR or 7z
	Files     []T
	Volumes   []int
	TotalSize int64
}

var trailingNumbersRegex = regexp.MustCompile(`\.\d+$`)

func stripTrailingNumbers(filename string) string {
	if loc := trailingNumbersRegex.FindStringIndex(filename); loc != nil {
		return filename[:loc[0]]
	}
	return filename
}

func getArchiveBaseName(filename string) (baseName string, fileType FileType) {
	lower := strings.ToLower(filename)

	if matches := rarPartNumberRegex.FindStringSubmatch(lower); len(matches) > 0 {
		return filename[:len(filename)-len(matches[0])], FileTypeRAR
	}

	if matches := rarRNumberRegex.FindStringSubmatch(lower); len(matches) > 0 {
		return filename[:len(filename)-len(matches[0])], FileTypeRAR
	}

	if matches := rarFirstPartRegex.FindStringSubmatch(lower); len(matches) > 0 {
		return filename[:len(filename)-len(matches[0])], FileTypeRAR
	}

	if matches := sevenzipPartNumberRegex.FindStringSubmatch(lower); len(matches) > 0 {
		return filename[:len(filename)-len(matches[0])], FileType7z
	}

	if matches := sevenzipFirstPartRegex.FindStringSubmatch(lower); len(matches) > 0 {
		return filename[:len(filename)-len(matches[0])], FileType7z
	}

	return "", FileTypePlain
}

type simpleFile interface {
	Name() string
	Size() int64
}

type typedArchiveFile interface {
	FileType() FileType
	Volume() int
}

func getFileVolume[T simpleFile](f T, fileType FileType) int {
	if tf, ok := any(f).(typedArchiveFile); ok {
		return tf.Volume()
	}
	switch fileType {
	case FileTypeRAR:
		return GetRARVolumeNumber(f.Name())
	case FileType7z:
		return Get7zVolumeNumber(f.Name())
	default:
		return -1
	}
}

func groupArchiveVolumes[T simpleFile](
	files []T,
) []archiveVolumeGroup[T] {
	groups := make(map[string]*archiveVolumeGroup[T])

	for _, f := range files {
		baseName, fileType := getArchiveBaseName(f.Name())
		aliased := false
		if fileType == FileTypePlain {
			if tf, ok := any(f).(typedArchiveFile); ok && tf.FileType() != FileTypePlain {
				fileType = tf.FileType()
				baseName = stripTrailingNumbers(f.Name())
				aliased = true
			} else {
				continue
			}
		}

		key := baseName + ":" + fileType.String()
		if g, ok := groups[key]; ok {
			g.Files = append(g.Files, f)
			g.TotalSize += f.Size()
		} else {
			groups[key] = &archiveVolumeGroup[T]{
				BaseName:  baseName,
				Aliased:   aliased,
				FileType:  fileType,
				Files:     []T{f},
				TotalSize: f.Size(),
			}
		}
	}

	result := make([]archiveVolumeGroup[T], 0, len(groups))
	for _, group := range groups {
		type indexedVolume struct {
			index  int
			volume int
		}
		ivs := make([]indexedVolume, len(group.Files))
		for i, f := range group.Files {
			ivs[i] = indexedVolume{index: i, volume: getFileVolume(f, group.FileType)}
		}
		slices.SortStableFunc(ivs, func(a, b indexedVolume) int {
			return a.volume - b.volume
		})
		sorted := make([]T, len(group.Files))
		volumes := make([]int, len(group.Files))
		for i, iv := range ivs {
			sorted[i] = group.Files[iv.index]
			volumes[i] = iv.volume
		}
		group.Files = sorted
		group.Volumes = volumes
		result = append(result, *group)
	}

	slices.SortStableFunc(result, func(a, b archiveVolumeGroup[T]) int {
		return cmp.Compare(b.TotalSize, a.TotalSize)
	})

	return result
}
