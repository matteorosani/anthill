// Package app holds Anthill's application services, such as ingestion,
// eligibility, claiming and the ant supervisor (design D1).
//
// Application services use the domain and depend on the interfaces in
// package ports, never on a concrete adapter.
package app
