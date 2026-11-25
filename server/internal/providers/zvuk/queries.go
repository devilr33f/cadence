package zvuk

// the site's own GraphQL operations (as zvuk.com's player and the
// zvuk-music library send them), cut to the fields this package reads

const searchQuery = `query search(
  $query: String
  $limit: Int = 2
  $tracks: Boolean = true
  $artists: Boolean = true
  $releases: Boolean = true
  $playlists: Boolean = true
) {
  search(query: $query) {
    tracks(limit: $limit) @include(if: $tracks) {
      items {
        id
        title
        duration
        artists {
          title
        }
        release {
          id
          title
          image {
            src
          }
        }
      }
    }
    artists(limit: $limit) @include(if: $artists) {
      items {
        id
        title
        image {
          src
        }
      }
    }
    releases(limit: $limit) @include(if: $releases) {
      items {
        id
        title
        image {
          src
        }
        artists {
          title
        }
      }
    }
    playlists(limit: $limit) @include(if: $playlists) {
      items {
        id
        title
        image {
          src
        }
      }
    }
  }
}`

const streamQuery = `query getStream($ids: [ID!]!) {
  mediaContents(ids: $ids) {
    __typename
    ... on Track {
      stream {
        expire
        expireDelta
        flac
        flacdrm
        high
        mid
      }
    }
  }
}`

const releasesQuery = `query getReleases($ids: [ID!]!, $relatedLimit: Int = 100) {
  getReleases(ids: $ids) {
    id
    title
    image {
      src
    }
    artists {
      title
    }
    tracks {
      id
      title
      duration
      artists {
        title
      }
      release {
        id
        title
        image {
          src
        }
      }
    }
    related(limit: $relatedLimit) {
      id
    }
  }
}`

const playlistTracksQuery = `query getPlaylistTracks($id: ID!, $limit: Int = 5, $offset: Int = 0) {
  playlistTracks(id: $id, limit: $limit, offset: $offset) {
    id
    title
    duration
    artists {
      title
    }
    release {
      id
      title
      image {
        src
      }
    }
  }
}`

const artistsQuery = `query getArtists(
  $ids: [ID!]!
  $withPopTracks: Boolean = false
  $tracksOffset: Int = 0
  $tracksLimit: Int = 100
) {
  getArtists(ids: $ids) {
    id
    popularTracks(offset: $tracksOffset, limit: $tracksLimit)
      @include(if: $withPopTracks) {
      id
      title
      duration
      artists {
        title
      }
      release {
        id
        title
        image {
          src
        }
      }
    }
  }
}`

const tracksQuery = `query getTracks($ids: [ID!]!) {
  getTracks(ids: $ids) {
    id
    release {
      id
    }
  }
}`
