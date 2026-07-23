package repository

type HealthRepository struct{}

func NewHealthRepository() HealthRepository {
	return HealthRepository{}
}
