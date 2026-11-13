package client_release

func mapCheckResponse(result CheckResult) CheckResponse {
	response := CheckResponse{
		Platform:     result.Platform,
		Status:       result.Status,
		CurrentBuild: result.CurrentBuild,
	}

	if result.MinSupported != nil {
		response.MinSupported = &MinSupportedReleaseResponse{
			Version: result.MinSupported.Version,
			Build:   result.MinSupported.Build,
		}
	}

	if result.Latest != nil {
		response.Latest = &LatestReleaseResponse{
			Version:       result.Latest.Version,
			Build:         result.Latest.Build,
			DownloadURL:   result.Latest.DownloadURL,
			SHA256:        result.Latest.SHA256,
			FileSizeBytes: result.Latest.FileSizeBytes,
			ReleaseNotes:  result.Latest.ReleaseNotes,
			PublishedAt:   result.Latest.PublishedAt,
		}
	}

	return response
}
