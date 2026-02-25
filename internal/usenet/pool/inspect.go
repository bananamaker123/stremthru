package usenet_pool

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/usenet/nzb"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/alitto/pond/v2"
	"github.com/nwaples/rardecode/v2"
)

var inspectLog = logger.Scoped("usenet/pool/inspect")

type NZBContentFileType string

const (
	NZBContentFileTypeVideo   NZBContentFileType = "video"
	NZBContentFileTypeArchive NZBContentFileType = "archive"
	NZBContentFileTypeOther   NZBContentFileType = "other"
	NZBContentFileTypeUnknown NZBContentFileType = ""
)

const (
	NZBContentFileErrorArticleNotFound = "article_not_found"
	NZBContentFileErrorOpenFailed      = "open_failed"
)

type NZBContentFile struct {
	Type       NZBContentFileType `json:"t"`
	Name       string             `json:"n"`
	Alias      string             `json:"alias,omitempty"`
	Size       int64              `json:"s"`
	Volume     int                `json:"vol,omitempty"`
	Streamable bool               `json:"strm"`
	Errors     []string           `json:"errs,omitempty"`
	Files      []NZBContentFile   `json:"files,omitempty"`
	Parts      []NZBContentFile   `json:"parts,omitempty"`
}

type NZBContent struct {
	Files      []NZBContentFile
	Streamable bool
}

func classifyNZBContentFileType(filename string) NZBContentFileType {
	if isVideoFile(filename) {
		return NZBContentFileTypeVideo
	}
	if IsArchiveFile(filename) {
		return NZBContentFileTypeArchive
	}
	return NZBContentFileTypeOther
}

func hasStreamableVideoInNZBContentFiles(files []NZBContentFile) bool {
	for i := range files {
		f := &files[i]
		name := f.Name
		if f.Alias != "" {
			name = f.Alias
		}
		isVideo := isVideoFile(name)
		if f.Streamable && isVideo {
			return true
		}
		if len(f.Files) > 0 && hasStreamableVideoInNZBContentFiles(f.Files) {
			return true
		}
	}
	return false
}

func isNZBStremable(c *NZBContent) bool {
	return hasStreamableVideoInNZBContentFiles(c.Files)
}

type nzbArchiveFile struct {
	filetype FileType
	name     string
	size     int64
	volume   int
}

func (f *nzbArchiveFile) Name() string {
	return f.name
}

func (f *nzbArchiveFile) Size() int64 {
	return f.size
}

func (f *nzbArchiveFile) FileType() FileType {
	return f.filetype
}

func (f *nzbArchiveFile) Volume() int {
	if f.volume >= 0 {
		return f.volume
	}
	switch f.filetype {
	case FileTypeRAR:
		return GetRARVolumeNumber(f.Name())
	case FileType7z:
		return Get7zVolumeNumber(f.Name())
	default:
		return -1
	}
}

func (p *Pool) InspectNZBContent(ctx context.Context, nzbDoc *nzb.NZB, password string) (*NZBContent, error) {
	content := &NZBContent{
		Files:      []NZBContentFile{},
		Streamable: true,
	}

	if len(nzbDoc.Files) == 0 {
		return content, nil
	}

	var nzbArchiveFiles []*nzbArchiveFile

	type segmentFetchResult struct {
		nzbFile      *nzb.File
		startSegment *SegmentData
		startErr     error
		endSegment   *SegmentData
		endErr       error
	}

	var needsFetch []*nzb.File

	for i := range nzbDoc.Files {
		f := &nzbDoc.Files[i]

		if f.SegmentCount() == 0 {
			content.Files = append(content.Files, NZBContentFile{
				Type:       NZBContentFileTypeOther,
				Name:       f.Name(),
				Size:       f.Size(),
				Streamable: false,
			})
			continue
		}

		needsFetch = append(needsFetch, f)
	}

	fetchResults := make([]segmentFetchResult, len(needsFetch))
	fetchPool := pond.NewPool(config.Newz.MaxConnectionPerStream)
	for i, f := range needsFetch {
		fetchPool.Submit(func() {
			startSegment, startErr := p.fetchSegment(ctx, &f.Segments[0], f.Groups)
			var endSegment *SegmentData
			var endErr error
			if f.SegmentCount() > 1 {
				endSegment, endErr = p.fetchSegment(ctx, &f.Segments[len(f.Segments)-1], f.Groups)
			}
			fetchResults[i] = segmentFetchResult{
				nzbFile:      f,
				startSegment: startSegment,
				startErr:     startErr,
				endSegment:   endSegment,
				endErr:       endErr,
			}
		})
	}
	fetchPool.StopAndWait()

	for _, fr := range fetchResults {
		filename := fr.nzbFile.Name()

		articleNotFound := errors.Is(fr.startErr, ErrArticleNotFound) || errors.Is(fr.endErr, ErrArticleNotFound)

		if isVideoFile(filename) {
			entry := NZBContentFile{
				Type:       NZBContentFileTypeVideo,
				Name:       filename,
				Size:       fr.nzbFile.Size(),
				Streamable: true,
			}
			if articleNotFound {
				entry.Streamable = false
				entry.Errors = append(entry.Errors, NZBContentFileErrorArticleNotFound)
			} else if fr.startErr != nil {
				entry.Streamable = false
				inspectLog.Warn("failed to fetch first segment for video file", "error", fr.startErr, "name", filename)
			} else if fr.endErr != nil {
				entry.Streamable = false
				inspectLog.Warn("failed to fetch last segment for video file", "error", fr.endErr, "name", filename)
			}
			content.Files = append(content.Files, entry)
			continue
		}

		if IsArchiveFile(filename) {
			if articleNotFound {
				content.Files = append(content.Files, NZBContentFile{
					Type:       NZBContentFileTypeArchive,
					Name:       filename,
					Size:       fr.nzbFile.Size(),
					Streamable: false,
					Errors:     []string{NZBContentFileErrorArticleNotFound},
				})
			} else {
				af := &nzbArchiveFile{
					filetype: DetectArchiveFileTypeByExtension(filename),
					name:     filename,
					size:     fr.nzbFile.Size(),
					volume:   -1,
				}
				if af.filetype == FileTypeRAR && fr.startSegment != nil {
					if vi, err := rardecode.ReadVolumeInfo(bytes.NewReader(fr.startSegment.Body), rardecode.SkipCheck, rardecode.IterHeadersOnly); err == nil {
						af.volume = vi.Number
					}
				}
				nzbArchiveFiles = append(nzbArchiveFiles, af)
			}
			continue
		}

		streamable := true
		var fileType FileType
		var errs []string

		if fr.startErr != nil {
			inspectLog.Warn("failed to fetch first segment for type detection", "error", fr.startErr, "name", filename)
			streamable = false
			if errors.Is(fr.startErr, ErrArticleNotFound) {
				errs = append(errs, NZBContentFileErrorArticleNotFound)
			}
		} else {
			fileType = DetectFileType(fr.startSegment.Body, filename)
			if fr.endErr != nil && errors.Is(fr.endErr, ErrArticleNotFound) {
				streamable = false
				errs = append(errs, NZBContentFileErrorArticleNotFound)
			}
		}

		switch fileType {
		case FileTypeRAR, FileType7z:
			if !streamable {
				content.Files = append(content.Files, NZBContentFile{
					Type:       NZBContentFileTypeArchive,
					Name:       filename,
					Size:       fr.nzbFile.Size(),
					Streamable: false,
					Errors:     errs,
				})
			} else {
				af := &nzbArchiveFile{
					filetype: fileType,
					name:     filename,
					size:     fr.nzbFile.Size(),
					volume:   -1,
				}
				if fileType == FileTypeRAR && fr.startSegment != nil {
					if vi, err := rardecode.ReadVolumeInfo(bytes.NewReader(fr.startSegment.Body), rardecode.SkipCheck, rardecode.IterHeadersOnly); err == nil {
						af.volume = vi.Number
					}
				}
				nzbArchiveFiles = append(nzbArchiveFiles, af)
			}
		case FileTypePlain:
			content.Files = append(content.Files, NZBContentFile{
				Type:       NZBContentFileTypeOther,
				Name:       filename,
				Size:       fr.nzbFile.Size(),
				Streamable: streamable,
			})
		default:
			content.Files = append(content.Files, NZBContentFile{
				Type:       NZBContentFileTypeUnknown,
				Name:       filename,
				Size:       fr.nzbFile.Size(),
				Streamable: streamable,
				Errors:     errs,
			})
		}
	}

	archiveGroups := groupArchiveVolumes(nzbArchiveFiles)

	for i := range archiveGroups {
		group := &archiveGroups[i]
		name := group.Files[0].Name()

		entry := NZBContentFile{
			Type: NZBContentFileTypeArchive,
			Name: name,
			Size: group.TotalSize,
		}

		ufs := NewUsenetFS(ctx, &UsenetFSConfig{
			NZB:               nzbDoc,
			Pool:              p,
			SegmentBufferSize: util.ToBytes("1MB"),
		})

		archiveName := name
		if group.Aliased {
			aliases := make(map[string]string, len(group.Files))
			for i, f := range group.Files {
				vol := group.Volumes[i]
				var syntheticName string
				switch group.FileType {
				case FileTypeRAR:
					syntheticName = GenerateRARVolumeName(group.BaseName, vol)
				case FileType7z:
					syntheticName = Generate7zVolumeName(group.BaseName, vol)
				}
				aliases[syntheticName] = f.Name()
				if vol == 0 {
					archiveName = syntheticName
					entry.Alias = syntheticName
				}
				entry.Parts = append(entry.Parts, NZBContentFile{
					Type:       NZBContentFileTypeArchive,
					Name:       f.Name(),
					Alias:      syntheticName,
					Size:       f.Size(),
					Volume:     vol,
					Streamable: true,
				})
			}
			ufs.SetAliases(aliases)
		} else {
			for i, f := range group.Files {
				entry.Parts = append(entry.Parts, NZBContentFile{
					Type:       NZBContentFileTypeArchive,
					Name:       f.Name(),
					Size:       f.Size(),
					Volume:     group.Volumes[i],
					Streamable: true,
				})
			}
		}

		var archive Archive
		switch group.FileType {
		case FileTypeRAR:
			archive = NewRARArchive(ufs, archiveName)
		case FileType7z:
			archive = NewSevenZipArchive(ufs.toAfero(), archiveName)
		}

		if err := archive.Open(password); err != nil {
			inspectLog.Warn("failed to open archive", "error", err, "name", name)
			if errors.Is(err, ErrArticleNotFound) {
				entry.Errors = append(entry.Errors, NZBContentFileErrorArticleNotFound)
			} else {
				entry.Errors = append(entry.Errors, NZBContentFileErrorOpenFailed)
			}
			content.Files = append(content.Files, entry)
			ufs.Close()
			continue
		}

		entry.Streamable = archive.IsStreamable()
		if entry.Streamable {
			files, err := archive.GetFiles()
			if err != nil {
				inspectLog.Warn("failed to get archive files", "name", name, "error", err)
				if errors.Is(err, ErrArticleNotFound) {
					entry.Errors = append(entry.Errors, NZBContentFileErrorArticleNotFound)
				} else {
					entry.Errors = append(entry.Errors, NZBContentFileErrorOpenFailed)
				}
			} else {
				entry.Files = p.inspectArchiveFiles(files, password)
			}
		}

		archive.Close()
		ufs.Close()
		content.Files = append(content.Files, entry)
	}

	content.Streamable = isNZBStremable(content)

	return content, nil
}

func (p *Pool) inspectArchiveFiles(files []ArchiveFile, password string) []NZBContentFile {
	archiveGroups := groupArchiveVolumes(files)

	if len(archiveGroups) == 0 {
		result := make([]NZBContentFile, len(files))
		for i, f := range files {
			result[i] = NZBContentFile{
				Type:       classifyNZBContentFileType(f.Name()),
				Name:       f.Name(),
				Size:       f.Size(),
				Streamable: f.IsStreamable(),
			}
		}
		return result
	}

	archiveFileNames := make(map[string]struct{})
	for i := range archiveGroups {
		for _, f := range archiveGroups[i].Files {
			archiveFileNames[f.Name()] = struct{}{}
		}
	}

	var result []NZBContentFile

	for _, f := range files {
		if _, isArchivePart := archiveFileNames[f.Name()]; !isArchivePart {
			result = append(result, NZBContentFile{
				Type:       classifyNZBContentFileType(f.Name()),
				Name:       f.Name(),
				Size:       f.Size(),
				Streamable: f.IsStreamable(),
			})
		}
	}

	for i := range archiveGroups {
		group := &archiveGroups[i]
		name := group.Files[0].Name()

		entry := NZBContentFile{
			Type: NZBContentFileTypeArchive,
			Name: name,
			Size: group.TotalSize,
		}
		for i, f := range group.Files {
			entry.Parts = append(entry.Parts, NZBContentFile{
				Type:       classifyNZBContentFileType(f.Name()),
				Name:       f.Name(),
				Size:       f.Size(),
				Volume:     group.Volumes[i],
				Streamable: true,
			})
		}

		allStreamable := true
		for _, f := range group.Files {
			if !f.IsStreamable() {
				allStreamable = false
				break
			}
		}

		if !allStreamable {
			result = append(result, entry)
			continue
		}

		afs := NewArchiveFS(group.Files)

		var innerArchive Archive
		switch group.FileType {
		case FileTypeRAR:
			innerArchive = NewRARArchive(afs, filepath.Base(name))
		case FileType7z:
			innerArchive = NewSevenZipArchive(afs.toAfero(), filepath.Base(name))
		default:
			afs.Close()
			result = append(result, entry)
			continue
		}

		if err := innerArchive.Open(""); err != nil {
			inspectLog.Warn("failed to open nested archive", "error", err, "name", name)
			if errors.Is(err, ErrArticleNotFound) {
				entry.Errors = append(entry.Errors, NZBContentFileErrorArticleNotFound)
			} else {
				entry.Errors = append(entry.Errors, NZBContentFileErrorOpenFailed)
			}
			afs.Close()
			result = append(result, entry)
			continue
		}

		entry.Streamable = innerArchive.IsStreamable()
		if entry.Streamable {
			if innerFiles, err := innerArchive.GetFiles(); err != nil {
				inspectLog.Warn("failed to get nested archive files", "error", err, "name", name)
				if errors.Is(err, ErrArticleNotFound) {
					entry.Errors = append(entry.Errors, NZBContentFileErrorArticleNotFound)
				} else {
					entry.Errors = append(entry.Errors, NZBContentFileErrorOpenFailed)
				}
			} else {
				innerContentFiles := make([]NZBContentFile, len(innerFiles))
				for j, f := range innerFiles {
					innerContentFiles[j] = NZBContentFile{
						Type:       classifyNZBContentFileType(f.Name()),
						Name:       f.Name(),
						Size:       f.Size(),
						Streamable: f.IsStreamable(),
					}
				}
				entry.Files = innerContentFiles
			}
		}

		innerArchive.Close()
		result = append(result, entry)
	}

	return result
}
