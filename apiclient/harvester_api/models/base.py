try:
    from typing import Final
except ImportError:
    # temporarily fix: https://github.com/harvester/tests/pull/982#issuecomment-1812846553
    # ref: https://github.com/python/typing_extensions/blob/cd9faac806f991344ade6c81cb5b321242f611c3/src/typing_extensions.py#L157 # noqa
    import typing

    class _Final(typing._FinalTypingBase, _root=True):  # type: ignore
        __slots__ = ('__type__',)

        def __init__(self, tp=None, **kwds):
            self.__type__ = tp

        def __getitem__(self, item):
            cls = type(self)
            if self.__type__ is None:
                return cls(typing._type_check(item,
                           f'{cls.__name__[1:]} accepts only single type.'),
                           _root=True)
            raise TypeError(f'{cls.__name__[1:]} cannot be further subscripted')

        def _eval_type(self, globalns, localns):
            new_tp = typing._eval_type(self.__type__, globalns, localns)
            if new_tp == self.__type__:
                return self
            return type(self)(new_tp, _root=True)

        def __repr__(self):
            r = super().__repr__()
            if self.__type__ is not None:
                r += f'[{typing._type_repr(self.__type__)}]'
            return r

        def __hash__(self):
            return hash((type(self).__name__, self.__type__))

        def __eq__(self, other):
            if not isinstance(other, _Final):
                return NotImplemented
            if self.__type__ is not None:
                return self.__type__ == other.__type__
            return self is other

    Final = _Final(_root=True)

MGMT_NETID: Final = object()
DEFAULT_STORAGE_CLS: Final = "harvester-longhorn"
