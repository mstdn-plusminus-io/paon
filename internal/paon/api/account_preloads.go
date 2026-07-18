package api

import "gorm.io/gorm"

func accountSerializerPreloads(query *gorm.DB) *gorm.DB {
	return query.
		Preload("AccountStat").
		Preload("User.Role").
		Preload("MovedToAccount.AccountStat").
		Preload("MovedToAccount.User.Role")
}

func accountRelationSerializerPreloads(query *gorm.DB, relation string) *gorm.DB {
	return query.
		Preload(relation + ".AccountStat").
		Preload(relation + ".User.Role").
		Preload(relation + ".MovedToAccount.AccountStat").
		Preload(relation + ".MovedToAccount.User.Role")
}
