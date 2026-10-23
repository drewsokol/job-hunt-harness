package normalize

// Place vocabulary for ParseLocation. Keys are lowercase. This is not a
// gazetteer: it covers the names job boards actually use. Add to it when a
// board's locations come out wrong.

var countries = map[string]string{
	"us": "US", "usa": "US", "u.s.": "US", "u.s.a.": "US", "united states": "US", "united states of america": "US", "america": "US",
	"uk": "GB", "u.k.": "GB", "united kingdom": "GB", "great britain": "GB", "england": "GB", "scotland": "GB", "wales": "GB", "northern ireland": "GB",
	"canada": "CA", "mexico": "MX", "brazil": "BR", "argentina": "AR", "chile": "CL", "colombia": "CO", "peru": "PE", "uruguay": "UY", "costa rica": "CR",
	"ireland": "IE", "germany": "DE", "france": "FR", "spain": "ES", "portugal": "PT", "italy": "IT", "netherlands": "NL", "the netherlands": "NL",
	"belgium": "BE", "switzerland": "CH", "austria": "AT", "sweden": "SE", "norway": "NO", "denmark": "DK", "finland": "FI", "iceland": "IS",
	"poland": "PL", "czech republic": "CZ", "czechia": "CZ", "romania": "RO", "hungary": "HU", "greece": "GR", "ukraine": "UA", "serbia": "RS",
	"bulgaria": "BG", "croatia": "HR", "estonia": "EE", "latvia": "LV", "lithuania": "LT", "luxembourg": "LU", "turkey": "TR", "türkiye": "TR",
	"israel": "IL", "united arab emirates": "AE", "uae": "AE", "saudi arabia": "SA", "egypt": "EG", "south africa": "ZA", "nigeria": "NG", "kenya": "KE",
	"india": "IN", "pakistan": "PK", "singapore": "SG", "japan": "JP", "south korea": "KR", "korea": "KR", "china": "CN", "hong kong": "HK", "taiwan": "TW",
	"qatar": "QA", "slovenia": "SI", "slovakia": "SK", "cyprus": "CY", "malta": "MT", "morocco": "MA", "ghana": "GH",
	"sri lanka": "LK", "bangladesh": "BD", "nepal": "NP",
	"philippines": "PH", "vietnam": "VN", "thailand": "TH", "malaysia": "MY", "indonesia": "ID",
	"australia": "AU", "new zealand": "NZ",
}

var regions = map[string]string{
	"emea": "EMEA", "europe": "Europe", "eu": "Europe", "apac": "APAC", "asia": "APAC", "asia pacific": "APAC", "latam": "LATAM",
	"latin america": "LATAM", "south america": "LATAM", "north america": "North America", "americas": "Americas", "amer": "Americas",
	"nordics": "Nordics", "dach": "DACH", "worldwide": "Worldwide", "global": "Worldwide", "international": "Worldwide",
}

var usStateCodes = []string{
	"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "FL", "GA", "HI", "ID", "IL", "IN", "IA", "KS", "KY", "LA", "ME", "MD",
	"MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH", "NJ", "NM", "NY", "NC", "ND", "OH", "OK", "OR", "PA", "RI", "SC",
	"SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI", "WY", "DC", "PR",
}

var usStateNames = map[string]string{
	"alabama": "AL", "alaska": "AK", "arizona": "AZ", "arkansas": "AR", "california": "CA", "colorado": "CO", "connecticut": "CT",
	"delaware": "DE", "florida": "FL", "georgia": "GA", "hawaii": "HI", "idaho": "ID", "illinois": "IL", "indiana": "IN", "iowa": "IA",
	"kansas": "KS", "kentucky": "KY", "louisiana": "LA", "maine": "ME", "maryland": "MD", "massachusetts": "MA", "michigan": "MI",
	"minnesota": "MN", "mississippi": "MS", "missouri": "MO", "montana": "MT", "nebraska": "NE", "nevada": "NV", "new hampshire": "NH",
	"new jersey": "NJ", "new mexico": "NM", "new york state": "NY", "north carolina": "NC", "north dakota": "ND", "ohio": "OH",
	"oklahoma": "OK", "oregon": "OR", "pennsylvania": "PA", "rhode island": "RI", "south carolina": "SC", "south dakota": "SD",
	"tennessee": "TN", "texas": "TX", "utah": "UT", "vermont": "VT", "virginia": "VA", "washington state": "WA", "west virginia": "WV",
	"wisconsin": "WI", "wyoming": "WY", "district of columbia": "DC", "puerto rico": "PR",
}

var caProvinceCodes = []string{"AB", "BC", "MB", "NB", "NL", "NS", "NT", "NU", "ON", "PE", "QC", "SK", "YT"}

var caProvinceNames = map[string]string{
	"alberta": "AB", "british columbia": "BC", "manitoba": "MB", "new brunswick": "NB", "newfoundland and labrador": "NL",
	"nova scotia": "NS", "northwest territories": "NT", "nunavut": "NU", "ontario": "ON", "prince edward island": "PE",
	"quebec": "QC", "québec": "QC", "saskatchewan": "SK", "yukon": "YT",
}

// cities are common job cities often written without a state or country.
var cities = map[string]Place{
	"new york": {"New York", "NY", "US"}, "new york city": {"New York", "NY", "US"}, "nyc": {"New York", "NY", "US"},
	"san francisco": {"San Francisco", "CA", "US"}, "sf": {"San Francisco", "CA", "US"}, "bay area": {"San Francisco Bay Area", "CA", "US"}, "sf bay area": {"San Francisco Bay Area", "CA", "US"},
	"san francisco bay area": {"San Francisco Bay Area", "CA", "US"}, "los angeles": {"Los Angeles", "CA", "US"},
	"san diego": {"San Diego", "CA", "US"}, "san jose": {"San Jose", "CA", "US"}, "palo alto": {"Palo Alto", "CA", "US"},
	"mountain view": {"Mountain View", "CA", "US"}, "seattle": {"Seattle", "WA", "US"}, "portland": {"Portland", "OR", "US"},
	"austin": {"Austin", "TX", "US"}, "dallas": {"Dallas", "TX", "US"}, "houston": {"Houston", "TX", "US"},
	"denver": {"Denver", "CO", "US"}, "boulder": {"Boulder", "CO", "US"}, "chicago": {"Chicago", "IL", "US"},
	"boston": {"Boston", "MA", "US"}, "cambridge, ma": {"Cambridge", "MA", "US"}, "atlanta": {"Atlanta", "GA", "US"},
	"miami": {"Miami", "FL", "US"}, "washington, dc": {"Washington", "DC", "US"}, "washington dc": {"Washington", "DC", "US"},
	"philadelphia": {"Philadelphia", "PA", "US"}, "pittsburgh": {"Pittsburgh", "PA", "US"}, "salt lake city": {"Salt Lake City", "UT", "US"},
	"phoenix": {"Phoenix", "AZ", "US"}, "minneapolis": {"Minneapolis", "MN", "US"}, "detroit": {"Detroit", "MI", "US"},
	"raleigh": {"Raleigh", "NC", "US"}, "nashville": {"Nashville", "TN", "US"},
	"toronto": {"Toronto", "ON", "CA"}, "vancouver": {"Vancouver", "BC", "CA"}, "montreal": {"Montreal", "QC", "CA"},
	"montréal": {"Montreal", "QC", "CA"}, "ottawa": {"Ottawa", "ON", "CA"}, "calgary": {"Calgary", "AB", "CA"}, "waterloo": {"Waterloo", "ON", "CA"},
	"london": {"London", "", "GB"}, "manchester": {"Manchester", "", "GB"}, "edinburgh": {"Edinburgh", "", "GB"},
	"dublin": {"Dublin", "", "IE"}, "berlin": {"Berlin", "", "DE"}, "munich": {"Munich", "", "DE"}, "hamburg": {"Hamburg", "", "DE"},
	"frankfurt": {"Frankfurt", "", "DE"}, "frankfurt am main": {"Frankfurt", "", "DE"}, "cologne": {"Cologne", "", "DE"}, "köln": {"Cologne", "", "DE"},
	"stuttgart": {"Stuttgart", "", "DE"}, "düsseldorf": {"Düsseldorf", "", "DE"}, "dusseldorf": {"Düsseldorf", "", "DE"},
	"paris": {"Paris", "", "FR"}, "amsterdam": {"Amsterdam", "", "NL"}, "madrid": {"Madrid", "", "ES"}, "barcelona": {"Barcelona", "", "ES"},
	"lisbon": {"Lisbon", "", "PT"}, "zurich": {"Zurich", "", "CH"}, "stockholm": {"Stockholm", "", "SE"}, "copenhagen": {"Copenhagen", "", "DK"},
	"warsaw": {"Warsaw", "", "PL"}, "tel aviv": {"Tel Aviv", "", "IL"},
	"bangalore": {"Bengaluru", "", "IN"}, "bengaluru": {"Bengaluru", "", "IN"}, "hyderabad": {"Hyderabad", "", "IN"}, "pune": {"Pune", "", "IN"},
	"tokyo": {"Tokyo", "", "JP"}, "seoul": {"Seoul", "", "KR"}, "sydney": {"Sydney", "", "AU"}, "melbourne": {"Melbourne", "", "AU"},
	"são paulo": {"São Paulo", "", "BR"}, "sao paulo": {"São Paulo", "", "BR"}, "mexico city": {"Mexico City", "", "MX"},
}

// stateNamesAfterComma are state names that are also cities or countries,
// so they only count as a state after a comma ("Seattle, Washington").
var stateNamesAfterComma = map[string]string{"washington": "WA", "new york": "NY", "georgia": "GA"}

// placeholders are location values that name no place.
var placeholders = map[string]bool{"n/a": true, "na": true, "tbd": true, "various": true, "multiple": true, "multiple locations": true, "flexible": true, "any": true}

// isoCodes are the country codes the vocabulary knows, accepted after a
// comma ("Dublin, IE") when they aren't also a state or province code.
var isoCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range countries {
		m[c] = true
	}
	return m
}()
