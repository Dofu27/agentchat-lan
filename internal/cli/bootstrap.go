package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

type bootstrapFile interface {
	io.Writer
	Sync() error
	Close() error
}
type bootstrapOps struct {
	create     func(string) (bootstrapFile, error)
	initialize func(context.Context, string, string) error
	syncParent func(string) error
}

func defaultBootstrapOps() bootstrapOps {
	return bootstrapOps{create: func(path string) (bootstrapFile, error) { return createBootstrapFile(path) }, initialize: hub.InitWithToken, syncParent: syncBootstrapParent}
}

func bootstrapHub(ctx context.Context, db, path string, resume bool, ops bootstrapOps) error {
	var token string
	if resume {
		// Keep the identity check tied to the opened object. Never follow a final
		// symlink, and never delete a credential on any failed initialization.
		before, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !before.Mode().IsRegular() {
			return errors.New("resume credential must be a regular file")
		}
		f, err := openResumeBootstrapFile(path)
		if err != nil {
			return err
		}
		info, statErr := f.Stat()
		if statErr != nil || !os.SameFile(before, info) {
			_ = f.Close()
			return errors.New("resume credential identity changed or unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 256))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		token = strings.TrimSpace(string(data))
	} else {
		token = protocol.NewToken("adm")
		f, err := ops.create(path)
		if err != nil {
			return fmt.Errorf("reserve admin credential: %w", err)
		}
		// All credential I/O precedes DB initialization. Even Close failure cannot
		// leave an initialized hub with an undisclosed admin credential.
		payload := token + "\n"
		n, writeErr := f.Write([]byte(payload))
		if writeErr == nil && n != len(payload) {
			writeErr = io.ErrShortWrite
		}
		var syncErr error
		if writeErr == nil {
			syncErr = f.Sync()
		}
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("credential persistence failed; this attempt did not commit an admin credential; inspect retained file %s: %w", path, err)
		}
		if ops.syncParent != nil {
			if err := ops.syncParent(path); err != nil {
				return fmt.Errorf("credential directory persistence failed; this attempt did not commit an admin credential; retained file %s: %w", path, err)
			}
		}
	}
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "adm_"))
	if !strings.HasPrefix(token, "adm_") || decodeErr != nil || len(decoded) != 32 {
		return errors.New("retained initializer credential is invalid; this attempt did not commit an admin credential")
	}
	if err := ops.initialize(ctx, db, token); err != nil {
		return fmt.Errorf("initialization failed; credential retained at %s; retry with --resume-init and the same --admin-token-file: %w", path, err)
	}
	return nil
}
