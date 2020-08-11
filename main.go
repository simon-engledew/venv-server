package main // import "github.com/simon-engledew/venv-server"

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker/cli/cli/command/image/build"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/pools"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func getContext(root string) (io.ReadCloser, error) {
	excludes, err := build.ReadDockerignore(root)
	if err != nil {
		return nil, fmt.Errorf("failed to read dockerignore: %w", err)
	}

	contextTar, err := archive.TarWithOptions(root, &archive.TarOptions{
		ExcludePatterns: build.TrimBuildFilesFromExcludes(excludes, "Dockerfile", false),
		ChownOpts:       &idtools.Identity{UID: 0, GID: 0},
	})
	if err != nil {
		return nil, err
	}

	return contextTar, nil
}

func closeOrPanic(format string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return
		}
		panic(fmt.Errorf(format, err))
	}
}

func rewriteTarHeaders(inputTarStream io.ReadCloser, fn func(*tar.Header)) io.ReadCloser {
	pipeReader, pipeWriter := io.Pipe()

	go func() {
		tarReader := tar.NewReader(inputTarStream)
		tarWriter := tar.NewWriter(pipeWriter)
		defer closeOrPanic("failed to close inputTarStream: %w", inputTarStream)
		defer closeOrPanic("failed to close tarWriter: %w", tarWriter)

		var err error
		var originalHeader *tar.Header
		for {
			originalHeader, err = tarReader.Next()
			if err == io.EOF {
				if err := tarWriter.Flush(); err != nil {
					log.Print(err)
				}
				break
			}
			if err != nil {
				if err := pipeWriter.CloseWithError(err); err != nil {
					panic(fmt.Errorf("failed to read next tar entry: %w", err))
				}
				return
			}

			fn(originalHeader)

			if err := tarWriter.WriteHeader(originalHeader); err != nil {
				if err := pipeWriter.CloseWithError(err); err != nil {
					panic(fmt.Errorf("failed to write header: %w", err))
				}
				return
			}
			if _, err := pools.Copy(tarWriter, tarReader); err != nil {
				if err := pipeWriter.CloseWithError(err); err != nil {
					panic(fmt.Errorf("failed to copy tarReader: %w", err))
				}
				return
			}
		}

		if err := pipeWriter.Close(); err != nil {
			panic(fmt.Errorf("failed to close pipeWriter: %w", err))
		}
	}()
	return pipeReader
}

func replaceFileInTar(path string, data []byte, inputTarStream io.ReadCloser) (io.ReadCloser, error) {
	now := time.Now()
	hdrTmpl := &tar.Header{
		Mode:       0600,
		Uid:        0,
		Gid:        0,
		ModTime:    now,
		Typeflag:   tar.TypeReg,
		AccessTime: now,
		ChangeTime: now,
	}

	inputTarStream = archive.ReplaceFileTarWrapper(inputTarStream, map[string]archive.TarModifierFunc{
		path: func(_ string, h *tar.Header, content io.Reader) (*tar.Header, []byte, error) {
			return hdrTmpl, data, nil
		},
	})
	return inputTarStream, nil
}

func main() {
	docker, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithVersion("1.38"),
	)

	if err != nil {
		log.Fatal(err)
	}

	defer closeOrPanic("could not close docker connection: %w", docker)

	mux := chi.NewMux()
	mux.Use(middleware.Logger)
	mux.Post("/{root}/*", func(w http.ResponseWriter, r *http.Request) {
		root := chi.URLParam(r, "root")
		root = filepath.Join("docker", root)

		if _, err := os.Stat(root); os.IsNotExist(err) {
			log.Printf("%s not found", root)
			http.NotFound(w, r)
			return
		}

		requirements, err := ioutil.ReadAll(r.Body)
		closeOrPanic("could not close response body: %w", r.Body)

		venv := filepath.Clean("/" + chi.URLParam(r, "*"))

		if venv == "." {
			http.NotFound(w, r)
			return
		}

		log.Printf("building venv at %s", venv)

		if err != nil {
			log.Print(err)
			http.Error(w, "could not read body", http.StatusBadRequest)
			return
		}

		contextTar, err := getContext(root)
		if err != nil {
			log.Print(err)
			http.Error(w, "could not get context", http.StatusInternalServerError)
			return
		}

		contextTar, err = replaceFileInTar("/requirements.txt", requirements, contextTar)
		if err != nil {
			log.Print(err)
			http.Error(w, "could not inject requirements", http.StatusInternalServerError)
			return
		}

		defer closeOrPanic("could not close context tar: %w", contextTar)

		venvDir := filepath.Dir(venv)

		response, err := docker.ImageBuild(r.Context(), contextTar, types.ImageBuildOptions{
			Remove: true, // remove intermediate steps
			BuildArgs: map[string]*string{
				"VENV": &venv,
			},
		})
		if err != nil {
			log.Print(err)
			http.Error(w, "could not build image", http.StatusInternalServerError)
			return
		}
		defer closeOrPanic("could not close response body: %w", response.Body)

		imageID := ""
		aux := func(msg jsonmessage.JSONMessage) {
			var result types.BuildResult
			if err := json.Unmarshal(*msg.Aux, &result); err != nil {
				panic(err)
			} else {
				imageID = result.ID
			}
		}

		err = jsonmessage.DisplayJSONMessagesStream(response.Body, os.Stdout, os.Stdout.Fd(), false, aux)
		if err != nil {
			log.Print(err)
			http.Error(w, "could stream build result", http.StatusInternalServerError)
			return
		}

		if imageID == "" {
			panic(fmt.Errorf("no image ID"))
		}

		init := true

		containerCreate, err := docker.ContainerCreate(
			r.Context(),
			&container.Config{
				Image:        imageID,
				Tty:          false,
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				OpenStdin:    false,
				StdinOnce:    true,
			},
			&container.HostConfig{
				AutoRemove:  false,
				Init:        &init,
				NetworkMode: "none",
			},
			&network.NetworkingConfig{},
			"",
		)

		if err != nil {
			log.Print(err)
			http.Error(w, "could stream create container", http.StatusInternalServerError)
			return
		}

		defer func() {
			if err := docker.ContainerRemove(context.Background(), containerCreate.ID, types.ContainerRemoveOptions{}); err != nil {
				log.Print(err)
			}
		}()

		data, _, err := docker.CopyFromContainer(r.Context(), containerCreate.ID, venv)
		if err != nil {
			log.Print(err)
			http.Error(w, "could copy from container", http.StatusInternalServerError)
			return
		}

		data = rewriteTarHeaders(data, func(header *tar.Header) {
			header.Name = strings.TrimPrefix(filepath.Join(venvDir, header.Name), "/")
		})

		defer closeOrPanic("failed to close data: %w", data)

		_, err = pools.Copy(w, data)

		if err != nil {
			log.Print(err)
		}
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	log.Printf("listening on %s", server.Addr)

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
