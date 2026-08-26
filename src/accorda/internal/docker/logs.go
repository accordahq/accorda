package docker

// AllLogLines is the Docker "tail" value requesting the full log history. It
// is shared by the Compose and image targets so both Docker log paths use one
// default (docs/ACCORDA.md §11).
const AllLogLines = "all"
