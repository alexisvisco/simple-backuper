package main

import (
	"context"
	"fmt"
	"github.com/gabriel-vasile/mimetype"
	"github.com/go-co-op/gocron/v2"
	"github.com/kelseyhightower/envconfig"
	nid "github.com/matoous/go-nanoid/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gopkg.in/yaml.v3"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

type SimpleBackuperConfig struct {
	Storage    ObjectStorageConfig
	ConfigPath string `envconfig:"CONFIG_PATH" required:"true"`
}

type ObjectStorageConfig struct {
	Endpoint         string `envconfig:"S3_ENDPOINT" required:"true"`
	Region           string `envconfig:"S3_REGION" required:"true"`
	Bucket           string `envconfig:"S3_BUCKET" required:"true"`
	SecretKey        string `envconfig:"S3_SECRET_KEY" required:"true"`
	AccessKey        string `envconfig:"S3_ACCESS_KEY" required:"true"`
	AutoCreateBucket bool   `envconfig:"S3_AUTO_CREATE_BUCKET" default:"false"`
}

type BackupRules struct {
	Jobs []BackupCommand `yaml:"jobs"`
}

func main() {
	var config SimpleBackuperConfig
	err := envconfig.Process("", &config)
	if err != nil {
		slog.Error("error parsing env variables", slog.String("error", err.Error()))
		return
	}

	cli, err := minio.New(config.Storage.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(
			config.Storage.AccessKey,
			config.Storage.SecretKey,
			""),
		Secure: true,
	})
	if err != nil {
		slog.Error("error creating minio client", slog.String("error", err.Error()))
		return
	}

	// check if bucket exists
	exists, err := cli.BucketExists(context.Background(), config.Storage.Bucket)
	if err != nil {
		slog.Error("error checking if bucket exists", slog.String("error", err.Error()))
		return
	}

	if !exists {
		slog.Error("bucket does not exist", slog.String("bucket", config.Storage.Bucket))
		if config.Storage.AutoCreateBucket {
			err = cli.MakeBucket(context.Background(), config.Storage.Bucket, minio.MakeBucketOptions{
				Region: config.Storage.Region,
			})
			slog.Info("bucket created", slog.String("bucket", config.Storage.Bucket),
				slog.String("region", config.Storage.Region))
		} else {
			return
		}
	}

	if err != nil {
		slog.Error("error creating bucket", slog.String("error", err.Error()))
		return
	}

	// parse config
	var backupRules BackupRules
	err = parseConfig(config.ConfigPath, &backupRules)
	if err != nil {
		slog.Error("error parsing config", slog.String("error", err.Error()))
		return
	}

	s, err := gocron.NewScheduler()
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		return
	}

	s.Start()

	for _, command := range backupRules.Jobs {
		_, err := s.NewJob(
			gocron.CronJob(command.Schedule, false),
			gocron.NewTask(command.Backup(cli, config.Storage.Bucket)),
		)

		if err != nil {
			slog.Error("error creating job", slog.String("error", err.Error()),
				slog.String("backup_name", command.Name))
			return
		}
	}

	slog.Info("starting scheduler")

	// keep the process running with only ctrl c to stop it
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, os.Kill)

	<-signals

	slog.Info("stopping scheduler")
}

func parseConfig(path string, b *BackupRules) error {
	fContent, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("error reading config fContent: %s", err)
	}
	err = yaml.Unmarshal(fContent, b)
	if err != nil {
		return fmt.Errorf("error parsing config fContent: %s", err)
	}

	return nil
}

type BackupCommand struct {
	Name             string   `yaml:"name"`
	Schedule         string   `yaml:"schedule"`
	Script           []string `yaml:"script"`
	FilepathToUpload string   `yaml:"filepath_to_upload"`
}

func (b BackupCommand) Backup(cli *minio.Client, bucketName string) func() {
	slog.Info("creating backup command", slog.String("backup_name", b.Name), slog.String("schedule", b.Schedule))

	return func() {
		id, _ := nid.Generate("1234567890abcdefghijklmnopqrstuvwxyz", 8)
		l := slog.With(
			slog.String("id", id),
			slog.String("backup_name", b.Name))

		l.Info("backup started")
		defer l.Info("backup finished")

		mkdirTemp, err := os.MkdirTemp(os.TempDir(), fmt.Sprintf("backup-%s-%s-", b.Name, id))
		if err != nil {
			l.Error("error creating temp dir", slog.String("error", err.Error()))
			return
		}

		mkdirTemp = strings.TrimRight(mkdirTemp, "/")
		for i, script := range b.Script {
			b.Script[i] = b.insertTemplate(script, id, mkdirTemp)
		}
		b.FilepathToUpload = b.insertTemplate(b.FilepathToUpload, id, mkdirTemp)

		command := exec.Command("sh", "-c", strings.Join(b.Script, " \n"))
		command.Stderr = NewCommandLoggger(l, true)
		command.Stdout = NewCommandLoggger(l, false)
		err = command.Run()
		if err != nil {
			l.Error("error running backup script", slog.String("error", err.Error()))
			return
		}

		// ensure that the file exists
		_, err = os.Stat(b.FilepathToUpload)
		if err != nil {
			l.Error("error checking if file exists", slog.String("error", err.Error()))
			return
		}

		// get the file extension
		ext := filepath.Ext(b.FilepathToUpload)

		newFileName := fmt.Sprintf("%s-%s-%s%s", time.Now().Format("2006_01_02_02_15_04_05"), b.Name, id, ext)

		mtype, err := mimetype.DetectFile(b.FilepathToUpload)
		if err != nil {
			l.Error("error detecting mimetype", slog.String("error", err.Error()))
			return
		}

		// upload to object storage
		_, err = cli.FPutObject(
			context.Background(),
			bucketName,
			newFileName,
			b.FilepathToUpload,
			minio.PutObjectOptions{
				ContentType: mtype.String(),
			})

		if err != nil {
			l.Error("error uploading file to object storage", slog.String("error", err.Error()))
			return
		}

		l.Info("backup uploaded to object storage",
			slog.String("file", newFileName),
			slog.String("bucket", bucketName))
	}
}

// insertTemplate replaces the template variables in the script
// ${BACKUP_ID} -> the id of the backup
// ${BACKUP_NAME} -> the name of the backup
// ${TEMP_DIR} -> the temp dir where the backup is stored
// And all the environment variables
func (b BackupCommand) insertTemplate(origin string, id string, mkdirTemp string) string {
	origin = strings.ReplaceAll(origin, "${BACKUP_ID}", id)
	origin = strings.ReplaceAll(origin, "${BACKUP_NAME}", b.Name)
	origin = strings.ReplaceAll(origin, "${TEMP_DIR}", mkdirTemp)

	for _, env := range os.Environ() {
		key := strings.Split(env, "=")[0]
		value := strings.Split(env, "=")[1]

		origin = strings.ReplaceAll(origin, fmt.Sprintf("${%s}", key), value)
	}

	return origin
}

type CommandLogger struct {
	l   *slog.Logger
	err bool
}

func NewCommandLoggger(l *slog.Logger, error bool) CommandLogger {
	return CommandLogger{
		l:   l,
		err: error,
	}
}

func (p CommandLogger) Write(x []byte) (n int, err error) {
	xy := strings.TrimRight(string(x), "\n")
	xy = strings.ReplaceAll(xy, "\n", "\\n")
	if p.err {
		p.l.Error("SCRIPT> " + string(xy))
	} else {
		p.l.Info("SCRIPT> " + string(xy))
	}

	return
}
