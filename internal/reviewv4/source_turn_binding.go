package reviewv4

import "errors"

// ResolveSourceTurnDependency returns the sole dependency containing ref's
// stable turn identity and, when present, exact SessionView snapshot.
func ResolveSourceTurnDependency(ref SourceTurnRef, dependencies []ChainDependency) (ChainDependency, error) {
	if err := validateSourceTurnRefShape(ref); err != nil {
		return ChainDependency{}, err
	}
	index, err := newSourceTurnBindingIndex(dependencies)
	if err != nil {
		return ChainDependency{}, err
	}
	return index.resolve(ref)
}

type sourceTurnBindingIndex struct {
	byTurn  map[string]sourceTurnMatch
	byExact map[string]ChainDependency
}

type sourceTurnMatch struct {
	dependency ChainDependency
	count      int
}

func newSourceTurnBindingIndex(dependencies []ChainDependency) (sourceTurnBindingIndex, error) {
	if len(dependencies) > 65536 {
		return sourceTurnBindingIndex{}, errors.New("chain dependencies exceed array limit")
	}
	index := sourceTurnBindingIndex{
		byTurn:  make(map[string]sourceTurnMatch),
		byExact: make(map[string]ChainDependency),
	}
	seenDependencies := make(map[string]bool, len(dependencies))
	for _, dependency := range dependencies {
		key := dependency.Provider + "\x00" + dependency.SessionID + "\x00" + dependency.SessionViewDigest
		if !validID(dependency.Provider) || !validID(dependency.SessionID) || !digestRE.MatchString(dependency.SessionViewDigest) || !digestRE.MatchString(dependency.DependencyDigest) || len(dependency.TurnUnitIDs) > 65536 || seenDependencies[key] {
			return sourceTurnBindingIndex{}, errors.New("invalid or duplicate chain dependency")
		}
		seenDependencies[key] = true
		local := make(map[string]bool, len(dependency.TurnUnitIDs))
		for _, turnID := range dependency.TurnUnitIDs {
			if !validID(turnID) || local[turnID] {
				return sourceTurnBindingIndex{}, errors.New("invalid or duplicate chain turn identity")
			}
			local[turnID] = true
			turnKey := sourceTurnBaseKey(dependency.Provider, dependency.SessionID, turnID)
			match := index.byTurn[turnKey]
			match.dependency = dependency
			match.count++
			index.byTurn[turnKey] = match
			index.byExact[sourceTurnExactKey(dependency.Provider, dependency.SessionID, turnID, dependency.SessionViewDigest)] = dependency
		}
	}
	return index, nil
}

func (index sourceTurnBindingIndex) resolve(ref SourceTurnRef) (ChainDependency, error) {
	if ref.SessionViewDigest != "" {
		dependency, ok := index.byExact[sourceTurnExactKey(ref.Provider, ref.SessionID, ref.TurnUnitID, ref.SessionViewDigest)]
		if !ok {
			return ChainDependency{}, errors.New("source turn reference has no matching chain dependency")
		}
		return dependency, nil
	}
	match := index.byTurn[sourceTurnBaseKey(ref.Provider, ref.SessionID, ref.TurnUnitID)]
	if match.count == 0 {
		return ChainDependency{}, errors.New("source turn reference has no matching chain dependency")
	}
	if match.count != 1 {
		return ChainDependency{}, errors.New("source turn reference is ambiguous across chain dependencies")
	}
	return match.dependency, nil
}

func validateSourceTurnRefShape(ref SourceTurnRef) error {
	if !validID(ref.Provider) || !validID(ref.SessionID) || !validID(ref.TurnUnitID) || (ref.SessionViewDigest != "" && !digestRE.MatchString(ref.SessionViewDigest)) {
		return errors.New("invalid source turn reference")
	}
	return nil
}

func sourceTurnBaseKey(provider, sessionID, turnUnitID string) string {
	return provider + "\x00" + sessionID + "\x00" + turnUnitID
}

func sourceTurnExactKey(provider, sessionID, turnUnitID, sessionViewDigest string) string {
	return sourceTurnBaseKey(provider, sessionID, turnUnitID) + "\x00" + sessionViewDigest
}
