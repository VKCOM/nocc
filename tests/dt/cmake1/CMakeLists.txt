cmake_minimum_required(VERSION 3.16)
project(cppe)

set(CMAKE_CXX_STANDARD 17)

if(APPLE)
    if(IS_DIRECTORY /opt/homebrew)
        include_directories(/opt/homebrew/include)
        link_directories(/opt/homebrew/lib)
    endif()
endif()

add_compile_options(-Wall -Wextra)
include_directories(src)
add_executable(example src/main.cpp src/my-math.cpp src/my-strings.cpp src/my-warning.cpp)
target_precompile_headers(example PRIVATE src/all-headers.h)

