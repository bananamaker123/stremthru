package nzb_info

import (
	"context"
	"time"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/job"
	"github.com/MunifTanjim/stremthru/internal/logger"
	usenetmanager "github.com/MunifTanjim/stremthru/internal/usenet/manager"
	"github.com/MunifTanjim/stremthru/internal/usenet/nzb"
	"github.com/MunifTanjim/stremthru/store"
)

const schedulerId = "process-nzb"

var log = logger.Scoped("job/" + schedulerId)

var scheduler = job.NewScheduler(&job.SchedulerConfig[JobData]{
	Id:           schedulerId,
	Title:        "Process NZB",
	RunExclusive: true,
	Queue:        queue,
	Executor: func(j *job.Scheduler[JobData]) error {
		j.JobQueue().Process(func(data JobData) error {
			nzbFile, err := fetchNZBFile(data.URL, data.Name, log, nil)
			if err != nil {
				return err
			}

			nzbDoc, err := nzb.ParseBytes(nzbFile.Blob)
			if err != nil {
				return err
			}

			hash := HashNZBFileLink(data.URL)

			name := data.Name
			if name == "" {
				name = nzbDoc.GetMeta("title")
			}
			if name == "" {
				name = nzbFile.Name
			}

			password := nzbDoc.GetMeta("password")
			if password == "" {
				password = data.Password
			}

			var nzbDate time.Time
			for _, f := range nzbDoc.Files {
				if f.Date > 0 {
					t := time.Unix(f.Date, 0)
					if nzbDate.IsZero() || t.Before(nzbDate) {
						nzbDate = t
					}
				}
			}

			info := &NZBInfo{
				Hash:      hash,
				Name:      name,
				Size:      nzbDoc.TotalSize(),
				FileCount: nzbDoc.FileCount(),
				Password:  password,
				URL:       data.URL,
				User:      data.User,
				Date:      db.Timestamp{Time: nzbDate},
				Status:    string(store.NewzStatusDownloading),
			}

			if err := Upsert(info); err != nil {
				return err
			}

			pool, err := usenetmanager.GetPool()
			if err != nil {
				return err
			}
			content, err := pool.InspectNZBContent(context.Background(), nzbDoc, password)
			if err != nil {
				log.Warn("failed to inspect nzb content", "error", err)
				UpdateStatus(hash, string(store.NewzStatusFailed))
				return err
			}
			info.ContentFiles.Data = content.Files
			info.Streamable = content.Streamable
			if content.Streamable {
				info.Status = string(store.NewzStatusDownloaded)
			} else {
				info.Status = string(store.NewzStatusFailed)
			}

			return Upsert(info)
		})
		return nil
	},
	ShouldSkip: func() bool {
		pool, err := usenetmanager.GetPool()
		return err != nil || pool.CountProviders() == 0
	},
})
