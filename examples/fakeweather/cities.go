package fakeweather

import "strings"

type cityProfile struct {
	Latitude  float64
	Longitude float64
	Elevation int // meters
	Zone      climateZone
	Polluted  bool // Elevated baseline AQI.
}

type locationCatalog[T any] map[string]T

// Earlier mentions win; at the same position, the more specific name wins.
func (l locationCatalog[T]) lookup(location string) (T, bool) {
	location = strings.ToLower(location)
	position := len(location)
	matchedLength := 0
	var result T
	for name, value := range l {
		index := strings.Index(location, name)
		if index < 0 || index > position || (index == position && len(name) <= matchedLength) {
			continue
		}
		position = index
		matchedLength = len(name)
		result = value
	}
	return result, matchedLength > 0
}

// Keys are lowercase substrings. Coordinates and elevations are approximate.
var knownCities = locationCatalog[cityProfile]{
	"beijing":   {39.9042, 116.4074, 43, zoneContinental, true},
	"tianjin":   {39.3434, 117.3616, 5, zoneContinental, false},
	"harbin":    {45.8038, 126.5340, 142, zoneContinental, false},
	"shenyang":  {41.8057, 123.4315, 45, zoneContinental, false},
	"shanghai":  {31.2304, 121.4737, 4, zoneSubtropical, true},
	"hangzhou":  {30.2741, 120.1551, 7, zoneSubtropical, false},
	"nanjing":   {32.0603, 118.7969, 20, zoneSubtropical, false},
	"wuhan":     {30.5928, 114.3055, 23, zoneSubtropical, false},
	"chengdu":   {30.5728, 104.0668, 500, zoneSubtropical, false},
	"chongqing": {29.4316, 106.9123, 244, zoneSubtropical, false},
	"guangzhou": {23.1291, 113.2644, 21, zoneSubtropical, false},
	"shenzhen":  {22.5431, 114.0579, 19, zoneSubtropical, false},
	"hong kong": {22.3193, 114.1694, 0, zoneSubtropical, false},
	"taipei":    {25.0330, 121.5654, 9, zoneSubtropical, false},
	"tokyo":     {35.6762, 139.6503, 40, zoneSubtropical, false},
	"osaka":     {34.6937, 135.5023, 24, zoneSubtropical, false},
	"kyoto":     {35.0116, 135.7681, 56, zoneSubtropical, false},
	"sapporo":   {43.0618, 141.3545, 19, zoneContinental, false},
	"seoul":     {37.5665, 126.9780, 38, zoneContinental, false},
	"busan":     {35.1796, 129.0756, 5, zoneSubtropical, false},

	"singapore":    {1.3521, 103.8198, 15, zoneTropical, false},
	"bangkok":      {13.7563, 100.5018, 1, zoneTropical, false},
	"kuala lumpur": {3.1390, 101.6869, 22, zoneTropical, false},
	"jakarta":      {-6.2088, 106.8456, 8, zoneTropical, false},
	"manila":       {14.5995, 120.9842, 16, zoneTropical, false},
	"ho chi minh":  {10.8231, 106.6297, 19, zoneTropical, false},
	"hanoi":        {21.0285, 105.8542, 16, zoneTropical, false},
	"yangon":       {16.8409, 96.1735, 23, zoneTropical, false},
	"phnom penh":   {11.5564, 104.9282, 12, zoneTropical, false},
	"colombo":      {6.9271, 79.8612, 1, zoneTropical, false},
	"mumbai":       {19.0760, 72.8777, 14, zoneTropical, true},
	"chennai":      {13.0827, 80.2707, 6, zoneTropical, false},
	"bangalore":    {12.9716, 77.5946, 920, zoneTropical, false},
	"kolkata":      {22.5726, 88.3639, 9, zoneTropical, false},
	"delhi":        {28.6139, 77.2090, 216, zoneSubtropical, true},
	"karachi":      {24.8607, 67.0011, 8, zoneSubtropical, false},
	"dhaka":        {23.8103, 90.4125, 4, zoneTropical, false},
	"lahore":       {31.5204, 74.3587, 217, zoneSubtropical, false},
	"islamabad":    {33.6844, 73.0479, 540, zoneSubtropical, false},
	"kathmandu":    {27.7172, 85.3240, 1400, zoneAlpine, false},
	"lhasa":        {29.6500, 91.1000, 3656, zoneAlpine, false},

	"dubai":     {25.2048, 55.2708, 5, zoneDesert, false},
	"abu dhabi": {24.4539, 54.3773, 27, zoneDesert, false},
	"riyadh":    {24.7136, 46.6753, 612, zoneDesert, false},
	"doha":      {25.2854, 51.5310, 10, zoneDesert, false},
	"kuwait":    {29.3759, 47.9774, 55, zoneDesert, false},
	"muscat":    {23.5859, 58.4059, 4, zoneDesert, false},
	"baghdad":   {33.3152, 44.3661, 34, zoneDesert, false},
	"tehran":    {35.6892, 51.3890, 1191, zoneSubtropical, false},
	"istanbul":  {41.0082, 28.9784, 39, zoneMediterranean, false},
	"jerusalem": {31.7683, 35.2137, 754, zoneMediterranean, false},
	"tel aviv":  {32.0853, 34.7818, 5, zoneMediterranean, false},
	"beirut":    {33.8938, 35.5018, 56, zoneMediterranean, false},

	"london":        {51.5074, -0.1278, 11, zoneOceanic, false},
	"paris":         {48.8566, 2.3522, 35, zoneOceanic, false},
	"dublin":        {53.3498, -6.2603, 20, zoneOceanic, false},
	"amsterdam":     {52.3676, 4.9041, 0, zoneOceanic, false},
	"brussels":      {50.8503, 4.3517, 13, zoneOceanic, false},
	"copenhagen":    {55.6761, 12.5683, 14, zoneOceanic, false},
	"hamburg":       {53.5511, 9.9937, 6, zoneOceanic, false},
	"berlin":        {52.5200, 13.4050, 34, zoneContinental, false},
	"munich":        {48.1351, 11.5820, 520, zoneContinental, false},
	"vienna":        {48.2082, 16.3738, 151, zoneContinental, false},
	"warsaw":        {52.2297, 21.0122, 113, zoneContinental, false},
	"prague":        {50.0755, 14.4378, 200, zoneContinental, false},
	"budapest":      {47.4979, 19.0402, 102, zoneContinental, false},
	"moscow":        {55.7558, 37.6173, 156, zoneContinental, false},
	"st petersburg": {59.9343, 30.3351, 13, zoneContinental, false},
	"kyiv":          {50.4501, 30.5234, 179, zoneContinental, false},
	"stockholm":     {59.3293, 18.0686, 28, zoneContinental, false},
	"oslo":          {59.9139, 10.7522, 23, zoneContinental, false},
	"helsinki":      {60.1699, 24.9384, 26, zoneContinental, false},
	"reykjavik":     {64.1466, -21.9426, 37, zonePolar, false},
	"rome":          {41.9028, 12.4964, 21, zoneMediterranean, false},
	"milan":         {45.4642, 9.1900, 120, zoneMediterranean, false},
	"athens":        {37.9838, 23.7275, 70, zoneMediterranean, false},
	"madrid":        {40.4168, -3.7038, 667, zoneMediterranean, false},
	"barcelona":     {41.3851, 2.1734, 12, zoneMediterranean, false},
	"lisbon":        {38.7223, -9.1393, 2, zoneMediterranean, false},
	"marseille":     {43.2965, 5.3698, 12, zoneMediterranean, false},
	"geneva":        {46.2044, 6.1432, 375, zoneAlpine, false},
	"innsbruck":     {47.2692, 11.4041, 574, zoneAlpine, false},

	"new york":      {40.7128, -74.0060, 10, zoneSubtropical, false},
	"boston":        {42.3601, -71.0589, 43, zoneContinental, false},
	"washington":    {38.9072, -77.0369, 125, zoneSubtropical, false},
	"philadelphia":  {39.9526, -75.1652, 12, zoneSubtropical, false},
	"chicago":       {41.8781, -87.6298, 181, zoneContinental, false},
	"toronto":       {43.6532, -79.3832, 76, zoneContinental, false},
	"montreal":      {45.5017, -73.5673, 36, zoneContinental, false},
	"winnipeg":      {49.8951, -97.1384, 232, zoneContinental, false},
	"miami":         {25.7617, -80.1918, 2, zoneSubtropical, false},
	"new orleans":   {29.9511, -90.0715, 1, zoneSubtropical, false},
	"atlanta":       {33.7490, -84.3880, 320, zoneSubtropical, false},
	"houston":       {29.7604, -95.3698, 24, zoneSubtropical, false},
	"dallas":        {32.7767, -96.7970, 131, zoneSubtropical, false},
	"los angeles":   {34.0522, -118.2437, 71, zoneMediterranean, false},
	"san francisco": {37.7749, -122.4194, 16, zoneMediterranean, false},
	"san diego":     {32.7157, -117.1611, 19, zoneMediterranean, false},
	"phoenix":       {33.4484, -112.0740, 331, zoneDesert, false},
	"las vegas":     {36.1699, -115.1398, 610, zoneDesert, false},
	"denver":        {39.7392, -104.9903, 1609, zoneAlpine, false},
	"seattle":       {47.6062, -122.3321, 56, zoneOceanic, false},
	"vancouver":     {49.2827, -123.1207, 70, zoneOceanic, false},
	"anchorage":     {61.2181, -149.9003, 31, zonePolar, false},
	"yellowknife":   {62.4540, -114.3718, 206, zonePolar, false},
	"mexico city":   {19.4326, -99.1332, 2240, zoneAlpine, true},

	"sao paulo":      {-23.5505, -46.6333, 760, zoneSubtropical, false},
	"rio de janeiro": {-22.9068, -43.1729, 5, zoneSubtropical, false},
	"buenos aires":   {-34.6037, -58.3816, 25, zoneSubtropical, false},
	"santiago":       {-33.4489, -70.6693, 570, zoneMediterranean, false},
	"lima":           {-12.0464, -77.0428, 154, zoneDesert, false},
	"bogota":         {4.7110, -74.0721, 2640, zoneAlpine, false},
	"quito":          {-0.1807, -78.4678, 2850, zoneAlpine, false},
	"la paz":         {-16.5000, -68.1500, 3640, zoneAlpine, false},
	"cusco":          {-13.5320, -71.9675, 3399, zoneAlpine, false},
	"caracas":        {10.4806, -66.9036, 900, zoneTropical, false},

	"cairo":        {30.0444, 31.2357, 23, zoneDesert, true},
	"lagos":        {6.5244, 3.3792, 11, zoneTropical, false},
	"nairobi":      {-1.2864, 36.8172, 1795, zoneAlpine, false},
	"johannesburg": {-26.2041, 28.0473, 1753, zoneAlpine, false},
	"cape town":    {-33.9249, 18.4241, 25, zoneMediterranean, false},
	"casablanca":   {33.5731, -7.5898, 27, zoneMediterranean, false},
	"algiers":      {36.7538, 3.0588, 224, zoneMediterranean, false},
	"tunis":        {36.8065, 10.1815, 4, zoneMediterranean, false},
	"addis ababa":  {9.0320, 38.7469, 2355, zoneAlpine, false},

	"sydney":     {-33.8688, 151.2093, 58, zoneSubtropical, false},
	"melbourne":  {-37.8136, 144.9631, 31, zoneOceanic, false},
	"brisbane":   {-27.4698, 153.0251, 27, zoneSubtropical, false},
	"perth":      {-31.9505, 115.8605, 0, zoneMediterranean, false},
	"auckland":   {-36.8485, 174.7633, 196, zoneOceanic, false},
	"wellington": {-41.2865, 174.7762, 13, zoneOceanic, false},

	"almaty":      {43.2389, 76.8897, 700, zoneContinental, false},
	"tashkent":    {41.2995, 69.2401, 455, zoneContinental, false},
	"ulaanbaatar": {47.8864, 106.9057, 1300, zoneContinental, false},
}

var regionalZones = locationCatalog[climateZone]{
	"sahara":     zoneDesert,
	"gobi":       zoneDesert,
	"antarctica": zonePolar,
	"alaska":     zonePolar,
	"murmansk":   zonePolar,
	"nuuk":       zonePolar,
	"svalbard":   zonePolar,
	"barrow":     zonePolar,
}
