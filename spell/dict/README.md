# Built-in dictionaries

`ge` built with the spell check (`-tags hunspell`) looks for the Hunspell
dictionary of a language in this order: `$GE_SPELL_DICT_DIR`, `~/.config/ge/spell/`, the
system directories (`/usr/share/hunspell`, ...), and then **here**.

To build a dictionary into `ge`, put the two files of a language in this
directory before building:

    en_US.aff
    en_US.dic

They must be UTF-8. Check the license of the dictionary you copy (the en_US
dictionary of the SCOWL project and the Hunspell dictionaries of LibreOffice
each have their own) and keep its license file next to it.

Without them the build still works; the dictionary then has to be installed on the
system or in `~/.config/ge/spell/`.
