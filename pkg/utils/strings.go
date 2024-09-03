package utils

func UniqueStrings(slice []string) []string {
	uniqueMap := make(map[string]bool)
	uniqueSlice := []string{}

	for _, item := range slice {
		if _, exists := uniqueMap[item]; !exists {
			uniqueMap[item] = true
			uniqueSlice = append(uniqueSlice, item)
		}
	}

	return uniqueSlice
}
