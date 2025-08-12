package provider

// TODO(mpokorny): Remove this shim by eliminating the wire logger singleton
// and making wire logging path injectable per test/config.
func InitWireLoggerForTests() {
	_ = getWireLogger()
}
