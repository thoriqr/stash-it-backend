package client_release

func mapAdminRelease(release Release) AdminReleaseResponse {
	return AdminReleaseResponse{
		Version:       release.Version,
		Build:         release.Build,
		DownloadURL:   release.DownloadURL,
		SHA256:        release.SHA256,
		FileSizeBytes: release.FileSizeBytes,
		ReleaseNotes:  release.ReleaseNotes,
		GitTag:        release.GitTag,
		PublishedAt:   release.PublishedAt,
	}
}

func mapReleaseRef(ref ReleaseRef) ReleaseRefResponse {
	return ReleaseRefResponse{
		Version: ref.Version,
		Build:   ref.Build,
	}
}

func mapReleasePolicy(policy ChannelPolicy) ReleasePolicyResponse {
	return ReleasePolicyResponse{
		Latest:       mapReleaseRef(policy.Latest),
		MinSupported: mapReleaseRef(policy.MinSupported),
	}
}

func mapPublishReleaseResponse(
	result PublishReleaseResult,
) PublishReleaseResponse {
	return PublishReleaseResponse{
		Release:      mapAdminRelease(result.Release),
		Latest:       mapReleaseRef(result.Policy.Latest),
		MinSupported: mapReleaseRef(result.Policy.MinSupported),
		Forced:       result.Forced,
	}
}

func mapListReleasesResponse(
	result ListReleasesResult,
) ListReleasesResponse {
	releases := make([]AdminReleaseResponse, 0, len(result.Releases))

	for _, release := range result.Releases {
		releases = append(releases, mapAdminRelease(release))
	}

	response := ListReleasesResponse{
		Releases: releases,
	}

	if result.Policy != nil {
		latest := mapReleaseRef(result.Policy.Latest)
		minSupported := mapReleaseRef(result.Policy.MinSupported)

		response.Latest = &latest
		response.MinSupported = &minSupported
	}

	return response
}
