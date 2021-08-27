package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/foxcpp/maddy/framework/config"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const modName = "storage.blob.s3"

type Store struct {
	instName string
	log      log.Logger

	endpoint string
	cl       *minio.Client

	bucketName   string
	objectPrefix string
}

func New(_, instName string, _, inlineArgs []string) (module.Module, error) {
	if len(inlineArgs) != 0 {
		return nil, fmt.Errorf("%s: expected 0 arguments", modName)
	}

	return &Store{
		instName: instName,
		log:      log.Logger{Name: modName},
	}, nil
}

func (s *Store) Init(cfg *config.Map) error {
	var (
		secure          bool
		accessKeyID     string
		secretAccessKey string
		location        string
	)
	cfg.String("endpoint", false, true, "", &s.endpoint)
	cfg.Bool("secure", false, true, &secure)
	cfg.String("access_key", false, true, "", &accessKeyID)
	cfg.String("secret_key", false, true, "", &secretAccessKey)
	cfg.String("bucket", false, true, "", &s.bucketName)
	cfg.String("region", false, false, "", &location)
	cfg.String("object_prefix", false, false, "", &s.objectPrefix)

	if _, err := cfg.Process(); err != nil {
		return err
	}
	if s.endpoint == "" {
		return fmt.Errorf("%s: endpoint not set", modName)
	}

	cl, err := minio.New(s.endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: secure,
		Region: location,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", modName, err)
	}

	s.cl = cl
	return nil
}

func (s *Store) Name() string {
	return modName
}

func (s *Store) InstanceName() string {
	return s.instName
}

type s3blob struct {
	pw      *io.PipeWriter
	didSync bool
	errCh   chan error
}

func (b *s3blob) Sync() error {
	// We do this in Sync instead of Close because
	// backend may not actually check the error of Close.
	// The problematic restriction is that Sync can now be called
	// only once.
	if b.didSync {
		panic("storage.blob.s3: Sync called twice for a blob object")
	}

	b.pw.Close()
	b.didSync = true
	return <-b.errCh
}

func (b *s3blob) Write(p []byte) (n int, err error) {
	return b.pw.Write(p)
}

func (b *s3blob) Close() error {
	if !b.didSync {
		b.pw.CloseWithError(fmt.Errorf("storage.blob.s3: blob closed without Sync"))
	}
	return nil
}

func (s *Store) Create(key string) (module.Blob, error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		_, err := s.cl.PutObject(context.TODO(), s.bucketName, s.objectPrefix+key, pr, -1, minio.PutObjectOptions{})
		if err != nil {
			pr.CloseWithError(fmt.Errorf("s3 PutObject: %w", err))
		}
		errCh <- err
	}()

	return &s3blob{
		pw:    pw,
		errCh: errCh,
	}, nil
}

func (s *Store) Open(key string) (io.ReadCloser, error) {
	obj, err := s.cl.GetObject(context.TODO(), s.bucketName, s.objectPrefix+key, minio.GetObjectOptions{})
	if err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.StatusCode == http.StatusNotFound {
			return nil, module.ErrNoSuchBlob
		}
		return nil, err
	}
	return obj, nil
}

func (s *Store) Delete(keys []string) error {
	var lastErr error
	for _, k := range keys {
		lastErr = s.cl.RemoveObject(context.TODO(), s.bucketName, s.objectPrefix+k, minio.RemoveObjectOptions{})
		if lastErr != nil {
			s.log.Error("failed to delete object", lastErr, s.objectPrefix+k)
		}
	}
	return lastErr
}

func init() {
	module.Register(modName, New)
}