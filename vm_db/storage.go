package vm_db

func (db *vmDb) GetValue(key []byte) ([]byte, error) {
	if value, ok := db.unsaved.GetValue(key); ok {
		return value, nil
	}
	return db.GetOriginalValue(key)
}
func (db *vmDb) GetOriginalValue(key []byte) ([]byte, error) {
	prevStateSnapshot, err := db.getPrevStateSnapshot()
	if err != nil {
		return nil, err
	}

	return prevStateSnapshot.GetValue(key)
}

func (db *vmDb) SetValue(key []byte, value []byte) {
	db.unsaved.SetValue(key, value)
}

func (db *vmDb) DeleteValue(key []byte) {
	db.unsaved.SetValue(key, []byte{})
}