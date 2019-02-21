// Copyright 2019 Sorint.lab
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied
// See the License for the specific language governing permissions and
// limitations under the License.

package executor

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/sorintlab/agola/internal/services/runservice/types"
	"go.uber.org/zap"
)

type taskSubmissionHandler struct {
	c chan<- *types.ExecutorTask
}

func NewTaskSubmissionHandler(c chan<- *types.ExecutorTask) *taskSubmissionHandler {
	return &taskSubmissionHandler{c: c}
}

func (h *taskSubmissionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var et *types.ExecutorTask
	d := json.NewDecoder(r.Body)

	if err := d.Decode(&et); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	h.c <- et
}

type logsHandler struct {
	log *zap.SugaredLogger
	e   *Executor
}

func NewLogsHandler(logger *zap.Logger, e *Executor) *logsHandler {
	return &logsHandler{
		log: logger.Sugar(),
		e:   e,
	}
}

func (h *logsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO(sgotti) Check authorized call from scheduler

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	taskID := r.URL.Query().Get("taskid")
	if taskID == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	s := r.URL.Query().Get("step")
	if s == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	step, err := strconv.Atoi(s)
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	follow := false
	_, ok := r.URL.Query()["follow"]
	if ok {
		follow = true
	}

	if err := h.readTaskLogs(taskID, step, w, follow); err != nil {
		h.log.Errorf("err: %+v", err)
	}
}

func (h *logsHandler) readTaskLogs(taskID string, step int, w http.ResponseWriter, follow bool) error {
	logPath := h.e.logPath(taskID, step)
	return h.readLogs(taskID, step, logPath, w, follow)
}

func (h *logsHandler) readLogs(taskID string, step int, logPath string, w http.ResponseWriter, follow bool) error {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "", http.StatusNotFound)
		} else {
			http.Error(w, "", http.StatusInternalServerError)
		}
		return err
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	br := bufio.NewReader(f)

	var flusher http.Flusher
	if fl, ok := w.(http.Flusher); ok {
		flusher = fl
	}
	stop := false
	flushstop := false
	for {
		if stop {
			return nil
		}
		data, err := br.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				return err
			}
			if !flushstop && follow {
				if _, err := f.Seek(-int64(len(data)), io.SeekCurrent); err != nil {
					return errors.Wrapf(err, "failed to seek in log file %q", logPath)
				}
				// check if the step is finished, is so flush until EOF and stop
				rt, ok := h.e.runningTasks.get(taskID)
				if !ok {
					flushstop = true
				} else {
					rt.Lock()
					if rt.et.Status.Steps[step].Phase.IsFinished() {
						flushstop = true
					}
					rt.Unlock()
				}
				// TODO(sgotti) use ionotify/fswatcher?
				time.Sleep(500 * time.Millisecond)
				continue
			} else {
				stop = true
			}
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

type archivesHandler struct {
	e *Executor
}

func NewArchivesHandler(e *Executor) *archivesHandler {
	return &archivesHandler{e: e}
}

func (h *archivesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO(sgotti) Check authorized call from scheduler

	taskID := r.URL.Query().Get("taskid")
	if taskID == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	s := r.URL.Query().Get("step")
	if s == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	step, err := strconv.Atoi(s)
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	w.Header().Set("Cache-Control", "no-cache")

	if err := h.readArchive(taskID, step, w); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "", http.StatusNotFound)
		} else {
			http.Error(w, "", http.StatusInternalServerError)
		}
		return
	}
}

func (h *archivesHandler) readArchive(taskID string, step int, w io.Writer) error {
	archivePath := h.e.archivePath(taskID, step)

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReader(f)

	_, err = io.Copy(w, br)
	return err
}
