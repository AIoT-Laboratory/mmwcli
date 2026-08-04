using System;
using System.Collections.Generic;

namespace MmwCli
{
    internal sealed class Arguments
    {
        private readonly List<string> _items;

        public Arguments(IEnumerable<string> args)
        {
            _items = new List<string>(args);
        }

        public int Count
        {
            get { return _items.Count; }
        }

        public string this[int index]
        {
            get { return _items[index]; }
        }

        public string Shift()
        {
            if (_items.Count == 0)
            {
                return null;
            }

            string value = _items[0];
            _items.RemoveAt(0);
            return value;
        }

        public bool TakeFlag(string name)
        {
            int index = _items.FindIndex(delegate(string item)
            {
                return string.Equals(item, name, StringComparison.OrdinalIgnoreCase);
            });

            if (index < 0)
            {
                return false;
            }

            _items.RemoveAt(index);
            return true;
        }

        public string TakeOption(string name, string defaultValue)
        {
            for (int index = 0; index < _items.Count; index++)
            {
                string item = _items[index];
                if (string.Equals(item, name, StringComparison.OrdinalIgnoreCase))
                {
                    if (index + 1 >= _items.Count)
                    {
                        throw new UsageException("选项缺少值: " + name);
                    }

                    string value = _items[index + 1];
                    _items.RemoveAt(index + 1);
                    _items.RemoveAt(index);
                    return value;
                }

                string prefix = name + "=";
                if (item.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
                {
                    string value = item.Substring(prefix.Length);
                    _items.RemoveAt(index);
                    return value;
                }
            }

            return defaultValue;
        }

        public int TakeIntOption(string name, int defaultValue, int minimum, int maximum)
        {
            string value = TakeOption(name, null);
            if (value == null)
            {
                return defaultValue;
            }

            int parsed;
            if (!int.TryParse(value, out parsed) || parsed < minimum || parsed > maximum)
            {
                throw new UsageException(string.Format(
                    "选项 {0} 必须是 {1}..{2} 的整数，收到: {3}",
                    name,
                    minimum,
                    maximum,
                    value));
            }

            return parsed;
        }

        public void EnsureEmpty()
        {
            if (_items.Count != 0)
            {
                throw new UsageException("无法识别的参数: " + string.Join(" ", _items.ToArray()));
            }
        }
    }

    internal sealed class UsageException : Exception
    {
        public UsageException(string message)
            : base(message)
        {
        }
    }
}
