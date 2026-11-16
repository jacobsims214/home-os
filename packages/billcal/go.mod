module home-os/billcal

go 1.25.0

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	home-os/icalx v0.0.0
)

require (
	github.com/emersion/go-ical v0.0.0-20261112170022-46c5b53a1723 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/teambition/rrule-go v1.8.2 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// Local module (also listed in go.work). The replace lets the apps' Docker
// builds run with GOWORK=off.
replace home-os/icalx => ../icalx
