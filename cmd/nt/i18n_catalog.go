package main

var messageCatalogs = map[string]map[messageID]string{
	"en":    catalogEnglish,
	"zh-CN": translatedCatalog(catalogEnglish, catalogZhCN),
	"zh-TW": translatedCatalog(catalogEnglish, catalogZhTW),
	"es":    translatedCatalog(catalogEnglish, catalogES),
	"fr":    translatedCatalog(catalogEnglish, catalogFR),
	"de":    translatedCatalog(catalogEnglish, catalogDE),
	"ja":    translatedCatalog(catalogEnglish, catalogJA),
	"ko":    translatedCatalog(catalogEnglish, catalogKO),
	"pt-BR": translatedCatalog(catalogEnglish, catalogPtBR),
	"ru":    translatedCatalog(catalogEnglish, catalogRU),
	"ar":    translatedCatalog(catalogEnglish, catalogAR),
	"hi":    translatedCatalog(catalogEnglish, catalogHI),
}

func translatedCatalog(base, translations map[messageID]string) map[messageID]string {
	result := make(map[messageID]string, len(base))
	for id, value := range base {
		result[id] = value
	}
	for id, value := range translations {
		result[id] = value
	}
	return result
}
