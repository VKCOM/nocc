// main.cpp
#include "all-headers.h"
#include "empty.h"

int main() {
  std::cout << sum(1, 2) << std::endl;
  std::cout << do_nothing_but_compiletime_warning() << std::endl;
  std::cout << concat("hello ", "nocc") << std::endl;

  return 0;
}

