package buildconfig

// SpecOptions are the per-parse inputs of Build::Rpm::parse that OBS stores
// in the config hash before parsing a recipe (bs_srcserver sets buildflavor
// and obspackage).
type SpecOptions struct {
	// BuildFlavor replaces @BUILD_FLAVOR@ in non-skipped lines.
	BuildFlavor string
	// ObsPackage replaces @OBS_PACKAGE@; empty leaves the placeholder alone.
	ObsPackage string
}

// SpecScope gives a WalkSpec visitor access to the live rpm macro table.
type SpecScope struct {
	eng *macroEngine
}

// Macro returns the body of macro name, or "" when it is not defined.
func (s *SpecScope) Macro(name string) string { return s.eng.macros[name] }

// SetMacro defines a plain (non-parametric) macro.
func (s *SpecScope) SetMacro(name, body string) {
	delete(s.eng.args, name)
	s.eng.macros[name] = body
}

// WalkSpec runs the generic part of Build::Rpm::parse over a recipe: macro
// expansion with the macros of cfg, %if/%ifarch/%ifos conditionals and
// @BUILD_FLAVOR@ substitution. visit is called, in order, for every line that
// is not skipped by a conditional (after expansion); it may update macros
// through the scope. The returned warnings are the diagnostics Perl prints to
// stderr, such as "cannot expand %(...)".
func WalkSpec(cfg *Config, content string, opt SpecOptions, visit func(line string, scope *SpecScope)) []string {
	st := &parseState{file: "", lineno: 0}
	var warnings []string
	warn := func(msg string) {
		st.warn(msg)
		warnings = append(warnings, st.parseError)
		st.parseError = ""
	}
	eng := newMacroEngine(cfg.Macros, warn)
	scope := &SpecScope{eng: eng}
	lines := splitLines(content)
	src := make([]inputLine, len(lines))
	for i, l := range lines {
		src[i] = inputLine{text: l}
	}
	p := &specParser{
		st: st, eng: eng, flavor: opt.BuildFlavor, obsPackage: opt.ObsPackage,
		visit: func(line string) { visit(line, scope) },
	}
	p.run(src)
	return warnings
}
