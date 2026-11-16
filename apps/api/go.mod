module home-os/api

go 1.25.0

require (
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/dexidp/dex/api/v2 v2.4.0
	github.com/go-chi/chi/v5 v5.3.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.52.0
	golang.org/x/time v0.12.0
	google.golang.org/grpc v1.76.0
	home-os/allergen v0.0.0
	home-os/billcal v0.0.0
	home-os/icalx v0.0.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/emersion/go-ical v0.0.0-20261112170022-46c5b53a1723 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.15.0 // indirect
	github.com/teambition/rrule-go v1.8.2 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251022142026-3a174f9686a8 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

exclude google.golang.org/genproto v0.0.0-20211208223120-3a66f561d7aa

// Local modules (also listed in go.work). The replaces let the Docker build
// (GOWORK=off, repository-root context) resolve it without the workspace.
replace home-os/allergen => ../../packages/allergen

replace home-os/icalx => ../../packages/icalx

replace home-os/billcal => ../../packages/billcal
