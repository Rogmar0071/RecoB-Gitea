// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

// ProjectMeta contains project metadata for API responses
// swagger:model
type ProjectMeta struct {
	// ID is the unique identifier for the project
	ID int64 `json:"id"`
	// Title is the title of the project
	Title string `json:"title"`
}
